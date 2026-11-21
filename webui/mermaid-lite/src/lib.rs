//! Native subset Mermaid renderer (#1368).
//!
//! Parses the two diagram types agents draw most — `flowchart`/`graph`
//! and `sequenceDiagram` — and lays them out into a [`Scene`]: plain
//! geometry plus text strings, with no markup. The cockpit maps the scene
//! to SVG elements and inserts every label as a text node, so diagram
//! text (agent-generated, untrusted) can never become HTML or script.
//! Interactive Mermaid directives (`click`, `href` callbacks) and styling
//! (`style`, `classDef`, `class`, `linkStyle`) are accepted and ignored.
//!
//! Anything outside the subset is a [`ParseError`] naming the line, and
//! the cockpit falls back to the source block with a note. Other diagram
//! types (class, state, ER, gantt…) are refused the same way.
//!
//! No dependencies, so the parser and layout run as host unit tests in CI.

use std::collections::HashMap;
use std::fmt;

/// Largest source accepted, in bytes.
pub const MAX_SOURCE: usize = 20_000;
/// Most flowchart nodes / sequence participants.
pub const MAX_NODES: usize = 150;
/// Most flowchart edges / sequence messages, notes and frames.
pub const MAX_ITEMS: usize = 400;

const FONT_W: f64 = 7.4;
const LINE_H: f64 = 17.0;

/// Why a block did not render. `line` is 1-based (0 = whole block).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ParseError {
    pub line: usize,
    pub message: String,
}

impl fmt::Display for ParseError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        if self.line == 0 {
            write!(f, "{}", self.message)
        } else {
            write!(f, "line {}: {}", self.line, self.message)
        }
    }
}

fn err(line: usize, message: impl Into<String>) -> ParseError {
    ParseError {
        line,
        message: message.into(),
    }
}

/// Visual role of a shape; the renderer maps it to a CSS class.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Role {
    Node,
    Edge,
    EdgeLabel,
    Actor,
    Lifeline,
    Message,
    Note,
    Frame,
    Arrow,
}

impl Role {
    pub fn class(self) -> &'static str {
        match self {
            Role::Node => "mmd-node",
            Role::Edge => "mmd-edge",
            Role::EdgeLabel => "mmd-edge-label",
            Role::Actor => "mmd-actor",
            Role::Lifeline => "mmd-lifeline",
            Role::Message => "mmd-message",
            Role::Note => "mmd-note-box",
            Role::Frame => "mmd-frame",
            Role::Arrow => "mmd-arrow",
        }
    }
}

/// Line style for strokes.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Stroke {
    Solid,
    Dashed,
    Thick,
}

/// One drawable. Coordinates are in scene units (CSS pixels at zoom 1).
#[derive(Clone, Debug, PartialEq)]
pub enum Shape {
    Rect { x: f64, y: f64, w: f64, h: f64, rx: f64, role: Role },
    Ellipse { cx: f64, cy: f64, rx: f64, ry: f64, role: Role },
    Polygon { points: Vec<(f64, f64)>, role: Role },
    Line { points: Vec<(f64, f64)>, stroke: Stroke, role: Role },
    /// Centred (or start-anchored) text, one entry per line.
    Text { x: f64, y: f64, lines: Vec<String>, start: bool, role: Role },
}

#[derive(Clone, Debug, PartialEq)]
pub struct Scene {
    /// "flowchart" or "sequence".
    pub kind: &'static str,
    pub width: f64,
    pub height: f64,
    pub shapes: Vec<Shape>,
}

impl Scene {
    /// Every text string in drawing order (tests, accessibility label).
    pub fn texts(&self) -> Vec<String> {
        self.shapes
            .iter()
            .filter_map(|s| match s {
                Shape::Text { lines, .. } => Some(lines.join(" ")),
                _ => None,
            })
            .collect()
    }
}

/// Parse and lay out a Mermaid block.
pub fn render(src: &str) -> Result<Scene, ParseError> {
    if src.len() > MAX_SOURCE {
        return Err(err(0, format!("diagram is larger than {MAX_SOURCE} bytes")));
    }
    let lines = statements(src);
    let Some((first_no, first)) = lines.first() else {
        return Err(err(0, "empty diagram"));
    };
    let head = first.split_whitespace().next().unwrap_or("");
    match head {
        "graph" | "flowchart" => {
            let dir = first.split_whitespace().nth(1).unwrap_or("TD");
            let dir = Dir::parse(dir).ok_or_else(|| err(*first_no, format!("unknown direction '{dir}'")))?;
            let flow = parse_flow(&lines[1..])?;
            Ok(layout_flow(&flow, dir))
        }
        "sequenceDiagram" => {
            if first.split_whitespace().count() > 1 {
                return Err(err(*first_no, "unexpected text after sequenceDiagram"));
            }
            let seq = parse_sequence(&lines[1..])?;
            Ok(layout_sequence(&seq))
        }
        other => Err(err(
            *first_no,
            format!("'{other}' diagrams are not supported yet (flowchart and sequenceDiagram are)"),
        )),
    }
}

/// Non-empty, non-comment statements with their 1-based line numbers.
/// Flowchart `;` separators split a line into several statements.
fn statements(src: &str) -> Vec<(usize, String)> {
    let mut out = Vec::new();
    let mut first = true;
    for (i, raw) in src.lines().enumerate() {
        let t = raw.trim();
        if t.is_empty() || t.starts_with("%%") {
            continue;
        }
        let is_seq_body = !first && out.first().is_some_and(|(_, h): &(usize, String)| h == "sequenceDiagram");
        if is_seq_body {
            out.push((i + 1, t.to_string()));
        } else {
            for part in split_semicolons(t) {
                let part = part.trim();
                if !part.is_empty() {
                    out.push((i + 1, part.to_string()));
                }
            }
        }
        first = false;
    }
    out
}

/// Split on `;` outside quotes and brackets.
fn split_semicolons(s: &str) -> Vec<&str> {
    let mut out = Vec::new();
    let mut depth = 0i32;
    let mut quoted = false;
    let mut start = 0;
    for (i, c) in s.char_indices() {
        match c {
            '"' => quoted = !quoted,
            '[' | '(' | '{' if !quoted => depth += 1,
            ']' | ')' | '}' if !quoted => depth -= 1,
            ';' if !quoted && depth <= 0 => {
                out.push(&s[start..i]);
                start = i + 1;
            }
            _ => {}
        }
    }
    out.push(&s[start..]);
    out
}

/// Label text: quotes stripped, `<br>` variants become line breaks,
/// anything else kept literally (rendered inert as text).
fn label_lines(raw: &str) -> Vec<String> {
    let mut t = raw.trim();
    if t.len() >= 2 && t.starts_with('"') && t.ends_with('"') {
        t = &t[1..t.len() - 1];
    }
    let mut text = t.to_string();
    for br in ["<br/>", "<br />", "<BR/>", "<BR />", "<BR>"] {
        text = text.replace(br, "<br>");
    }
    text.split("<br>")
        .map(|l| l.trim().to_string())
        .collect::<Vec<_>>()
}

fn text_w(lines: &[String]) -> f64 {
    lines
        .iter()
        .map(|l| l.chars().count() as f64 * FONT_W)
        .fold(0.0, f64::max)
}

// ---------------------------------------------------------------- flowchart

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Dir {
    TD,
    BT,
    LR,
    RL,
}

impl Dir {
    fn parse(s: &str) -> Option<Dir> {
        match s {
            "TD" | "TB" => Some(Dir::TD),
            "BT" => Some(Dir::BT),
            "LR" => Some(Dir::LR),
            "RL" => Some(Dir::RL),
            _ => None,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum NodeShape {
    Rect,
    Round,
    Circle,
    Diamond,
}

#[derive(Clone, Debug, PartialEq)]
pub struct FlowNode {
    pub id: String,
    pub label: Vec<String>,
    pub shape: NodeShape,
}

#[derive(Clone, Debug, PartialEq)]
pub struct FlowEdge {
    pub from: usize,
    pub to: usize,
    pub label: Vec<String>,
    pub stroke: Stroke,
    pub arrow: bool,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct Flow {
    pub nodes: Vec<FlowNode>,
    pub edges: Vec<FlowEdge>,
}

impl Flow {
    fn node(&mut self, line: usize, id: &str, decl: Option<(Vec<String>, NodeShape)>) -> Result<usize, ParseError> {
        let idx = match self.nodes.iter().position(|n| n.id == id) {
            Some(i) => i,
            None => {
                if self.nodes.len() >= MAX_NODES {
                    return Err(err(line, format!("more than {MAX_NODES} nodes")));
                }
                self.nodes.push(FlowNode {
                    id: id.to_string(),
                    label: vec![id.to_string()],
                    shape: NodeShape::Rect,
                });
                self.nodes.len() - 1
            }
        };
        if let Some((label, shape)) = decl {
            self.nodes[idx].label = label;
            self.nodes[idx].shape = shape;
        }
        Ok(idx)
    }
}

fn is_id_char(c: char) -> bool {
    c.is_alphanumeric() || c == '_' || c == '-' || c == '.'
}

/// Statements accepted and ignored: styling and interactivity.
fn ignored_flow_statement(s: &str) -> bool {
    let word = s.split_whitespace().next().unwrap_or("");
    matches!(
        word,
        "style" | "classDef" | "class" | "linkStyle" | "click" | "direction" | "end" | "accTitle" | "accDescr"
    ) || word == "subgraph"
}

/// Read a node reference at the start of `s`: `id` plus an optional
/// shape-and-label. Returns the id, the declaration and the rest.
fn read_node(line: usize, s: &str) -> Result<(String, Option<(Vec<String>, NodeShape)>, &str), ParseError> {
    let s = s.trim_start();
    let id_end = s.char_indices().find(|(_, c)| !is_id_char(*c)).map(|(i, _)| i).unwrap_or(s.len());
    // An id may not swallow the dashes of a following arrow (`A-->B`).
    let mut id_end = id_end;
    for op in ["--", "-.", "=="] {
        if let Some(p) = s[..id_end].find(op) {
            id_end = id_end.min(p);
        }
    }
    let id = s[..id_end].trim_end_matches(['-', '.']);
    if id.is_empty() {
        return Err(err(line, format!("expected a node id at '{}'", clip(s))));
    }
    let id_end = id.len();
    let rest = &s[id_end..];
    let shapes: [(&str, &str, NodeShape); 8] = [
        ("((", "))", NodeShape::Circle),
        ("([", "])", NodeShape::Round),
        ("[[", "]]", NodeShape::Rect),
        ("[(", ")]", NodeShape::Rect),
        ("[", "]", NodeShape::Rect),
        ("(", ")", NodeShape::Round),
        ("{", "}", NodeShape::Diamond),
        (">", "]", NodeShape::Rect),
    ];
    for (open, close, shape) in shapes {
        if let Some(body) = rest.strip_prefix(open) {
            let end = find_close(body, close).ok_or_else(|| err(line, format!("unclosed '{open}' in node {id}")))?;
            let label = label_lines(&body[..end]);
            return Ok((id.to_string(), Some((label, shape)), &body[end + close.len()..]));
        }
    }
    Ok((id.to_string(), None, rest))
}

/// Index of `close` in `body`, skipping a quoted label.
fn find_close(body: &str, close: &str) -> Option<usize> {
    let t = body.trim_start();
    if t.starts_with('"') {
        let lead = body.len() - t.len();
        let q = t[1..].find('"')? + 1 + lead;
        return body[q + 1..].find(close).map(|i| i + q + 1);
    }
    body.find(close)
}

fn clip(s: &str) -> String {
    let t: String = s.chars().take(24).collect();
    if s.chars().count() > 24 {
        format!("{t}…")
    } else {
        t
    }
}

/// Read an edge operator (with optional inline/pipe label) at the start of
/// `s`. Returns `None` when `s` does not start with an arrow.
fn read_edge(line: usize, s: &str) -> Result<Option<(Vec<String>, Stroke, bool, &str)>, ParseError> {
    let s = s.trim_start();
    let ops: [(&str, Stroke, bool); 13] = [
        ("-.->", Stroke::Dashed, true),
        ("-.-", Stroke::Dashed, false),
        ("==>", Stroke::Thick, true),
        ("===", Stroke::Thick, false),
        ("-->", Stroke::Solid, true),
        ("--o", Stroke::Solid, true),
        ("--x", Stroke::Solid, true),
        ("---", Stroke::Solid, false),
        ("<-->", Stroke::Solid, true),
        ("~~~", Stroke::Dashed, false),
        ("--", Stroke::Solid, false),
        ("==", Stroke::Thick, false),
        ("-.", Stroke::Dashed, false),
    ];
    for (op, stroke, arrow) in ops {
        let Some(mut rest) = s.strip_prefix(op) else {
            continue;
        };
        // Extend runs like `---->`.
        while let Some(r) = rest.strip_prefix('-').or_else(|| rest.strip_prefix('=')) {
            rest = r;
        }
        let mut arrow = arrow;
        if let Some(r) = rest.strip_prefix('>') {
            rest = r;
            arrow = true;
        }
        // `A -- text --> B` / `A -. text .-> B` / `A == text ==> B`.
        if matches!(op, "--" | "==" | "-.") && !arrow {
            let closers = ["-->", "---", "==>", "===", ".->", "-.-"];
            if let Some((pos, closer)) = closers.iter().filter_map(|c| rest.find(c).map(|p| (p, *c))).min() {
                let label = label_lines(&rest[..pos]);
                let after = &rest[pos + closer.len()..];
                let arrow = closer.ends_with('>');
                let stroke = if closer.contains('.') || op == "-." {
                    Stroke::Dashed
                } else if closer.contains('=') {
                    Stroke::Thick
                } else {
                    Stroke::Solid
                };
                return Ok(Some((label, stroke, arrow, after)));
            }
            return Err(err(line, "edge label is missing its closing arrow"));
        }
        let rest_t = rest.trim_start();
        if let Some(body) = rest_t.strip_prefix('|') {
            let end = body.find('|').ok_or_else(|| err(line, "unclosed '|' edge label"))?;
            return Ok(Some((label_lines(&body[..end]), stroke, arrow, &body[end + 1..])));
        }
        return Ok(Some((Vec::new(), stroke, arrow, rest)));
    }
    Ok(None)
}

fn parse_flow(lines: &[(usize, String)]) -> Result<Flow, ParseError> {
    let mut flow = Flow::default();
    for (line, stmt) in lines {
        let line = *line;
        if ignored_flow_statement(stmt) {
            continue;
        }
        // A statement is a chain of node groups joined by edges; a group
        // is one or more nodes joined by `&`.
        let mut rest: &str = stmt;
        let mut prev: Vec<usize> = Vec::new();
        let mut pending: Option<(Vec<String>, Stroke, bool)> = None;
        loop {
            let mut group = Vec::new();
            loop {
                let (id, decl, r) = read_node(line, rest)?;
                group.push(flow.node(line, &id, decl)?);
                rest = r.trim_start();
                if let Some(r) = rest.strip_prefix('&') {
                    rest = r;
                    continue;
                }
                break;
            }
            if let Some((label, stroke, arrow)) = pending.take() {
                for &from in &prev {
                    for &to in &group {
                        if flow.edges.len() >= MAX_ITEMS {
                            return Err(err(line, format!("more than {MAX_ITEMS} edges")));
                        }
                        flow.edges.push(FlowEdge {
                            from,
                            to,
                            label: label.clone(),
                            stroke,
                            arrow,
                        });
                    }
                }
            }
            prev = group;
            if rest.is_empty() {
                break;
            }
            match read_edge(line, rest)? {
                Some((label, stroke, arrow, r)) => {
                    pending = Some((label, stroke, arrow));
                    rest = r;
                }
                None => return Err(err(line, format!("unexpected '{}'", clip(rest)))),
            }
        }
        if pending.is_some() {
            return Err(err(line, "edge has no target node"));
        }
    }
    if flow.nodes.is_empty() {
        return Err(err(0, "flowchart has no nodes"));
    }
    Ok(flow)
}

struct Placed {
    x: f64,
    y: f64,
    w: f64,
    h: f64,
}

fn node_size(n: &FlowNode) -> (f64, f64) {
    let tw = text_w(&n.label);
    let th = n.label.len() as f64 * LINE_H;
    match n.shape {
        NodeShape::Rect | NodeShape::Round => ((tw + 28.0).max(64.0), th + 18.0),
        NodeShape::Diamond => ((tw + 28.0).max(60.0) * 1.45, (th + 18.0) * 1.6),
        NodeShape::Circle => {
            let d = (tw + 24.0).max(th + 24.0).max(48.0);
            (d, d)
        }
    }
}

/// Ranks by longest path over forward edges; edges that close a cycle
/// (found by DFS in declaration order) are ignored for ranking.
fn ranks(flow: &Flow) -> Vec<usize> {
    let n = flow.nodes.len();
    let mut out_edges = vec![Vec::new(); n];
    for (i, e) in flow.edges.iter().enumerate() {
        out_edges[e.from].push((e.to, i));
    }
    let mut back = vec![false; flow.edges.len()];
    let mut state = vec![0u8; n]; // 0 new, 1 on stack, 2 done
    let mut order = Vec::with_capacity(n);
    for start in 0..n {
        if state[start] != 0 {
            continue;
        }
        let mut stack = vec![(start, 0usize)];
        state[start] = 1;
        while let Some(&mut (v, ref mut k)) = stack.last_mut() {
            if *k < out_edges[v].len() {
                let (w, ei) = out_edges[v][*k];
                *k += 1;
                match state[w] {
                    0 => {
                        state[w] = 1;
                        stack.push((w, 0));
                    }
                    1 => back[ei] = true,
                    _ => {}
                }
            } else {
                state[v] = 2;
                order.push(v);
                stack.pop();
            }
        }
    }
    order.reverse(); // topological order of the forward graph
    let mut rank = vec![0usize; n];
    for &v in &order {
        for &(w, ei) in &out_edges[v] {
            if !back[ei] && w != v {
                rank[w] = rank[w].max(rank[v] + 1);
            }
        }
    }
    rank
}

fn layout_flow(flow: &Flow, dir: Dir) -> Scene {
    const RANK_GAP: f64 = 56.0;
    const NODE_GAP: f64 = 28.0;
    const PAD: f64 = 16.0;
    let rank = ranks(flow);
    let max_rank = rank.iter().copied().max().unwrap_or(0);
    let mut layers: Vec<Vec<usize>> = vec![Vec::new(); max_rank + 1];
    for (i, r) in rank.iter().enumerate() {
        layers[*r].push(i);
    }
    // One barycentre sweep: order each layer by the mean position of its
    // predecessors, keeping declaration order on ties.
    for r in 1..layers.len() {
        let pos: HashMap<usize, usize> = layers[r - 1].iter().enumerate().map(|(p, v)| (*v, p)).collect();
        let mut keyed: Vec<(f64, usize, usize)> = layers[r]
            .iter()
            .enumerate()
            .map(|(k, &v)| {
                let preds: Vec<f64> = flow
                    .edges
                    .iter()
                    .filter(|e| e.to == v)
                    .filter_map(|e| pos.get(&e.from).map(|p| *p as f64))
                    .collect();
                let bary = if preds.is_empty() { k as f64 } else { preds.iter().sum::<f64>() / preds.len() as f64 };
                (bary, k, v)
            })
            .collect();
        keyed.sort_by(|a, b| a.0.partial_cmp(&b.0).unwrap_or(std::cmp::Ordering::Equal).then(a.1.cmp(&b.1)));
        layers[r] = keyed.into_iter().map(|(_, _, v)| v).collect();
    }
    let sizes: Vec<(f64, f64)> = flow.nodes.iter().map(node_size).collect();
    let vertical = matches!(dir, Dir::TD | Dir::BT);
    // Along = rank axis, across = within-rank axis.
    let along = |(w, h): (f64, f64)| if vertical { h } else { w };
    let across = |(w, h): (f64, f64)| if vertical { w } else { h };
    let layer_thick: Vec<f64> = layers.iter().map(|l| l.iter().map(|v| along(sizes[*v])).fold(0.0, f64::max)).collect();
    let layer_span: Vec<f64> = layers
        .iter()
        .map(|l| l.iter().map(|v| across(sizes[*v])).sum::<f64>() + NODE_GAP * (l.len().saturating_sub(1)) as f64)
        .collect();
    let span = layer_span.iter().copied().fold(0.0, f64::max);
    let total_along: f64 = layer_thick.iter().sum::<f64>() + RANK_GAP * (layers.len().saturating_sub(1)) as f64;
    let mut placed: Vec<Placed> = (0..flow.nodes.len()).map(|_| Placed { x: 0.0, y: 0.0, w: 0.0, h: 0.0 }).collect();
    let mut a = 0.0;
    for (r, layer) in layers.iter().enumerate() {
        let mut c = (span - layer_span[r]) / 2.0;
        let mid_along = a + layer_thick[r] / 2.0;
        for &v in layer {
            let (w, h) = sizes[v];
            let mid_across = c + across(sizes[v]) / 2.0;
            let along_pos = match dir {
                Dir::BT | Dir::RL => total_along - mid_along,
                _ => mid_along,
            };
            let (cx, cy) = if vertical { (mid_across, along_pos) } else { (along_pos, mid_across) };
            placed[v] = Placed { x: cx + PAD, y: cy + PAD, w, h };
            c += across(sizes[v]) + NODE_GAP;
        }
        a += layer_thick[r] + RANK_GAP;
    }
    let (width, height) = if vertical { (span, total_along) } else { (total_along, span) };
    let mut shapes = Vec::new();
    // Edges first so nodes paint over their ends.
    for e in &flow.edges {
        let (s, t) = (&placed[e.from], &placed[e.to]);
        if e.from == e.to {
            let x = s.x + s.w / 2.0;
            let pts = vec![(x, s.y - 6.0), (x + 22.0, s.y - 6.0), (x + 22.0, s.y + 6.0), (x, s.y + 6.0)];
            if e.arrow {
                shapes.push(arrow_head(pts[2], pts[3]));
            }
            shapes.push(Shape::Line { points: pts, stroke: e.stroke, role: Role::Edge });
            continue;
        }
        let from = clip_to(s, flow.nodes[e.from].shape, (t.x, t.y));
        let to = clip_to(t, flow.nodes[e.to].shape, (s.x, s.y));
        shapes.push(Shape::Line { points: vec![from, to], stroke: e.stroke, role: Role::Edge });
        if e.arrow {
            shapes.push(arrow_head(from, to));
        }
        if !e.label.is_empty() && e.label.iter().any(|l| !l.is_empty()) {
            let (mx, my) = ((from.0 + to.0) / 2.0, (from.1 + to.1) / 2.0);
            let w = text_w(&e.label) + 10.0;
            let h = e.label.len() as f64 * LINE_H + 4.0;
            shapes.push(Shape::Rect { x: mx - w / 2.0, y: my - h / 2.0, w, h, rx: 3.0, role: Role::EdgeLabel });
            shapes.push(text_block(mx, my, e.label.clone(), Role::EdgeLabel));
        }
    }
    for (i, n) in flow.nodes.iter().enumerate() {
        let p = &placed[i];
        let shape = match n.shape {
            NodeShape::Rect => Shape::Rect { x: p.x - p.w / 2.0, y: p.y - p.h / 2.0, w: p.w, h: p.h, rx: 2.0, role: Role::Node },
            NodeShape::Round => Shape::Rect { x: p.x - p.w / 2.0, y: p.y - p.h / 2.0, w: p.w, h: p.h, rx: p.h / 2.0, role: Role::Node },
            NodeShape::Circle => Shape::Ellipse { cx: p.x, cy: p.y, rx: p.w / 2.0, ry: p.h / 2.0, role: Role::Node },
            NodeShape::Diamond => Shape::Polygon {
                points: vec![(p.x, p.y - p.h / 2.0), (p.x + p.w / 2.0, p.y), (p.x, p.y + p.h / 2.0), (p.x - p.w / 2.0, p.y)],
                role: Role::Node,
            },
        };
        shapes.push(shape);
        shapes.push(text_block(p.x, p.y, n.label.clone(), Role::Node));
    }
    Scene {
        kind: "flowchart",
        width: width + PAD * 2.0,
        height: height + PAD * 2.0,
        shapes,
    }
}

/// Where the segment from node centre towards `toward` leaves the node.
fn clip_to(p: &Placed, shape: NodeShape, toward: (f64, f64)) -> (f64, f64) {
    let (dx, dy) = (toward.0 - p.x, toward.1 - p.y);
    if dx == 0.0 && dy == 0.0 {
        return (p.x, p.y);
    }
    let (hw, hh) = (p.w / 2.0, p.h / 2.0);
    let t = match shape {
        NodeShape::Rect | NodeShape::Round => {
            let tx = if dx != 0.0 { hw / dx.abs() } else { f64::INFINITY };
            let ty = if dy != 0.0 { hh / dy.abs() } else { f64::INFINITY };
            tx.min(ty)
        }
        NodeShape::Diamond => 1.0 / (dx.abs() / hw + dy.abs() / hh),
        NodeShape::Circle => 1.0 / ((dx / hw).powi(2) + (dy / hh).powi(2)).sqrt(),
    };
    (p.x + dx * t, p.y + dy * t)
}

fn arrow_head(from: (f64, f64), to: (f64, f64)) -> Shape {
    let (dx, dy) = (to.0 - from.0, to.1 - from.1);
    let len = (dx * dx + dy * dy).sqrt().max(1e-6);
    let (ux, uy) = (dx / len, dy / len);
    let (bx, by) = (to.0 - ux * 9.0, to.1 - uy * 9.0);
    let (px, py) = (-uy * 4.5, ux * 4.5);
    Shape::Polygon {
        points: vec![to, (bx + px, by + py), (bx - px, by - py)],
        role: Role::Arrow,
    }
}

fn text_block(cx: f64, cy: f64, lines: Vec<String>, role: Role) -> Shape {
    let top = cy - (lines.len() as f64 - 1.0) * LINE_H / 2.0;
    Shape::Text { x: cx, y: top, lines, start: false, role }
}

// ----------------------------------------------------------------- sequence

#[derive(Clone, Debug, PartialEq)]
pub struct Participant {
    pub id: String,
    pub label: Vec<String>,
}

#[derive(Clone, Debug, PartialEq)]
pub enum SeqItem {
    Message { from: usize, to: usize, text: Vec<String>, stroke: Stroke, head: bool },
    Note { from: usize, to: usize, side: NoteSide, text: Vec<String> },
    Frame { label: String },
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum NoteSide {
    Left,
    Right,
    Over,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct Sequence {
    pub participants: Vec<Participant>,
    pub items: Vec<SeqItem>,
}

impl Sequence {
    fn participant(&mut self, line: usize, id: &str) -> Result<usize, ParseError> {
        let id = id.trim();
        if id.is_empty() || id.contains(char::is_whitespace) && !id.starts_with('"') {
            return Err(err(line, format!("bad participant name '{}'", clip(id))));
        }
        if let Some(i) = self.participants.iter().position(|p| p.id == id) {
            return Ok(i);
        }
        if self.participants.len() >= MAX_NODES {
            return Err(err(line, format!("more than {MAX_NODES} participants")));
        }
        self.participants.push(Participant {
            id: id.to_string(),
            label: label_lines(id),
        });
        Ok(self.participants.len() - 1)
    }

    fn push(&mut self, line: usize, item: SeqItem) -> Result<(), ParseError> {
        if self.items.len() >= MAX_ITEMS {
            return Err(err(line, format!("more than {MAX_ITEMS} messages")));
        }
        self.items.push(item);
        Ok(())
    }
}

fn parse_sequence(lines: &[(usize, String)]) -> Result<Sequence, ParseError> {
    let mut seq = Sequence::default();
    let mut autonumber = false;
    let mut count = 0usize;
    for (line, stmt) in lines {
        let line = *line;
        let word = stmt.split_whitespace().next().unwrap_or("");
        let rest = stmt[word.len()..].trim();
        match word {
            "participant" | "actor" => {
                let (id, alias) = match rest.split_once(" as ") {
                    Some((id, alias)) => (id.trim(), Some(alias.trim())),
                    None => (rest, None),
                };
                let i = seq.participant(line, id)?;
                if let Some(alias) = alias {
                    seq.participants[i].label = label_lines(alias);
                }
                continue;
            }
            "autonumber" => {
                autonumber = true;
                continue;
            }
            "activate" | "deactivate" | "title" | "accTitle" | "accDescr" | "end" | "box" | "link" | "links"
            | "create" | "destroy" => continue,
            "loop" | "alt" | "else" | "opt" | "par" | "and" | "critical" | "option" | "break" | "rect" => {
                let label = if rest.is_empty() { word.to_string() } else { format!("{word} [{rest}]") };
                seq.push(line, SeqItem::Frame { label })?;
                continue;
            }
            _ => {}
        }
        if word.eq_ignore_ascii_case("note") {
            let (place, text) = rest
                .split_once(':')
                .ok_or_else(|| err(line, "note needs ': text'"))?;
            let place = place.trim();
            let (side, who) = if let Some(w) = place.strip_prefix("right of ") {
                (NoteSide::Right, w)
            } else if let Some(w) = place.strip_prefix("left of ") {
                (NoteSide::Left, w)
            } else if let Some(w) = place.strip_prefix("over ") {
                (NoteSide::Over, w)
            } else {
                return Err(err(line, format!("unknown note position '{}'", clip(place))));
            };
            let mut ids = who.split(',');
            let a = seq.participant(line, ids.next().unwrap_or(""))?;
            let b = match ids.next() {
                Some(id) => seq.participant(line, id)?,
                None => a,
            };
            seq.push(line, SeqItem::Note { from: a, to: b, side, text: label_lines(text) })?;
            continue;
        }
        let (head, text) = match stmt.split_once(':') {
            Some((h, t)) => (h, t),
            None => (stmt.as_str(), ""),
        };
        let arrows: [(&str, Stroke, bool); 8] = [
            ("-->>", Stroke::Dashed, true),
            ("->>", Stroke::Solid, true),
            ("--x", Stroke::Dashed, true),
            ("-x", Stroke::Solid, true),
            ("--)", Stroke::Dashed, true),
            ("-)", Stroke::Solid, true),
            ("-->", Stroke::Dashed, false),
            ("->", Stroke::Solid, false),
        ];
        let Some((pos, op, stroke, has_head)) = arrows
            .iter()
            .filter_map(|(op, s, h)| head.find(op).map(|p| (p, *op, *s, *h)))
            .min_by_key(|(p, op, _, _)| (*p, std::cmp::Reverse(op.len())))
        else {
            return Err(err(line, format!("unexpected '{}'", clip(stmt))));
        };
        let from = head[..pos].trim();
        let to = head[pos + op.len()..].trim().trim_start_matches(['+', '-']).trim();
        let from = seq.participant(line, from)?;
        let to = seq.participant(line, to)?;
        let mut text = label_lines(text);
        if autonumber {
            count += 1;
            if let Some(first) = text.first_mut() {
                *first = format!("{count}. {first}");
            }
        }
        seq.push(line, SeqItem::Message { from, to, text, stroke, head: has_head })?;
    }
    if seq.participants.is_empty() {
        return Err(err(0, "sequence diagram has no participants"));
    }
    Ok(seq)
}

fn layout_sequence(seq: &Sequence) -> Scene {
    const PAD: f64 = 16.0;
    const BOX_H: f64 = 36.0;
    let box_w: Vec<f64> = seq.participants.iter().map(|p| (text_w(&p.label) + 24.0).max(80.0)).collect();
    let longest_msg = seq
        .items
        .iter()
        .filter_map(|i| match i {
            SeqItem::Message { text, from, to, .. } if from != to => Some(text_w(text)),
            _ => None,
        })
        .fold(0.0, f64::max);
    let gap = (longest_msg + 40.0).clamp(150.0, 380.0);
    let mut centers = Vec::with_capacity(box_w.len());
    let mut x = PAD + box_w.first().copied().unwrap_or(80.0) / 2.0;
    for i in 0..box_w.len() {
        if i > 0 {
            x += gap.max((box_w[i - 1] + box_w[i]) / 2.0 + 20.0);
        }
        centers.push(x);
    }
    let mut shapes = Vec::new();
    let mut y = PAD + BOX_H + 18.0;
    let mut rows = Vec::new();
    let left = PAD;
    let right_edge = centers.last().copied().unwrap_or(0.0) + box_w.last().copied().unwrap_or(80.0) / 2.0;
    for item in &seq.items {
        match item {
            SeqItem::Message { from, to, text, stroke, head } => {
                let lines = text.len().max(1) as f64;
                let (x1, x2) = (centers[*from], centers[*to]);
                if from == to {
                    let top = y + lines * LINE_H;
                    rows.push(text_start(x1 + 8.0, y + LINE_H * 0.7, text.clone()));
                    let pts = vec![(x1, top), (x1 + 34.0, top), (x1 + 34.0, top + 18.0), (x1, top + 18.0)];
                    if *head {
                        rows.push(arrow_head(pts[2], pts[3]));
                    }
                    rows.push(Shape::Line { points: pts, stroke: *stroke, role: Role::Message });
                    y = top + 32.0;
                } else {
                    let line_y = y + lines * LINE_H + 4.0;
                    rows.push(text_block((x1 + x2) / 2.0, y + LINE_H * 0.7, text.clone(), Role::Message));
                    rows.push(Shape::Line { points: vec![(x1, line_y), (x2, line_y)], stroke: *stroke, role: Role::Message });
                    if *head {
                        rows.push(arrow_head((x1, line_y), (x2, line_y)));
                    }
                    y = line_y + 18.0;
                }
            }
            SeqItem::Note { from, to, side, text } => {
                let w = (text_w(text) + 20.0).max(70.0);
                let h = text.len() as f64 * LINE_H + 12.0;
                let (a, b) = (centers[*from].min(centers[*to]), centers[*from].max(centers[*to]));
                let (nx, nw) = match side {
                    NoteSide::Right => (a + 10.0, w),
                    NoteSide::Left => (a - 10.0 - w, w),
                    NoteSide::Over => {
                        let nw = w.max(b - a + 40.0);
                        ((a + b) / 2.0 - nw / 2.0, nw)
                    }
                };
                rows.push(Shape::Rect { x: nx, y, w: nw, h, rx: 2.0, role: Role::Note });
                rows.push(text_block(nx + nw / 2.0, y + 6.0 + LINE_H * 0.7, text.clone(), Role::Note));
                y += h + 14.0;
            }
            SeqItem::Frame { label } => {
                let lines = vec![label.clone()];
                rows.push(Shape::Line { points: vec![(left, y), (right_edge, y)], stroke: Stroke::Dashed, role: Role::Frame });
                rows.push(text_start(left + 4.0, y + LINE_H * 0.8, lines));
                y += LINE_H + 12.0;
            }
        }
    }
    let bottom = y + 6.0;
    let mut min_x = PAD;
    let mut max_x = right_edge;
    for row in &rows {
        if let Shape::Rect { x, w, .. } = row {
            min_x = min_x.min(*x);
            max_x = max_x.max(x + w);
        }
    }
    for (i, p) in seq.participants.iter().enumerate() {
        let (cx, w) = (centers[i], box_w[i]);
        shapes.push(Shape::Line {
            points: vec![(cx, PAD + BOX_H), (cx, bottom)],
            stroke: Stroke::Dashed,
            role: Role::Lifeline,
        });
        for top in [PAD, bottom] {
            shapes.push(Shape::Rect { x: cx - w / 2.0, y: top, w, h: BOX_H, rx: 2.0, role: Role::Actor });
            shapes.push(text_block(cx, top + BOX_H / 2.0, p.label.clone(), Role::Actor));
        }
    }
    shapes.extend(rows);
    // Shift everything right when a left note spills past the margin.
    let shift = if min_x < PAD { PAD - min_x } else { 0.0 };
    if shift > 0.0 {
        for s in &mut shapes {
            shift_shape(s, shift);
        }
    }
    Scene {
        kind: "sequence",
        width: max_x + shift + PAD,
        height: bottom + BOX_H + PAD,
        shapes,
    }
}

fn text_start(x: f64, y: f64, lines: Vec<String>) -> Shape {
    Shape::Text { x, y, lines, start: true, role: Role::Message }
}

fn shift_shape(s: &mut Shape, dx: f64) {
    match s {
        Shape::Rect { x, .. } | Shape::Text { x, .. } => *x += dx,
        Shape::Ellipse { cx, .. } => *cx += dx,
        Shape::Polygon { points, .. } | Shape::Line { points, .. } => {
            for p in points {
                p.0 += dx;
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn flow(src: &str) -> Flow {
        let lines = statements(src);
        parse_flow(&lines[1..]).expect(src)
    }

    fn labels(f: &Flow) -> Vec<String> {
        f.nodes.iter().map(|n| n.label.join("\n")).collect()
    }

    #[test]
    fn valid_flowchart_renders() {
        let scene = render("flowchart TD\n  A[Start] --> B{Ok?}\n  B -->|yes| C(Done)\n  B -- no --> D((Retry))\n  D -.-> A").unwrap();
        assert_eq!(scene.kind, "flowchart");
        assert!(scene.width > 0.0 && scene.height > 0.0);
        let texts = scene.texts();
        for want in ["Start", "Ok?", "Done", "Retry", "yes", "no"] {
            assert!(texts.iter().any(|t| t == want), "missing {want}: {texts:?}");
        }
        let nodes = scene.shapes.iter().filter(|s| matches!(s, Shape::Rect { role: Role::Node, .. } | Shape::Polygon { role: Role::Node, .. } | Shape::Ellipse { role: Role::Node, .. })).count();
        assert_eq!(nodes, 4);
    }

    #[test]
    fn flowchart_shapes_labels_and_edges_parse() {
        let f = flow("graph LR\nA[\"Quoted [label]\"] --> B(Round)\nB --> C{Choice}\nC --> D((Circle))\nC ==> E([Stadium])\nE --- F[[Sub]]\nF -.-> G>Flag]");
        assert_eq!(labels(&f), vec!["Quoted [label]", "Round", "Choice", "Circle", "Stadium", "Sub", "Flag"]);
        let shapes: Vec<NodeShape> = f.nodes.iter().map(|n| n.shape).collect();
        assert_eq!(
            shapes,
            vec![NodeShape::Rect, NodeShape::Round, NodeShape::Diamond, NodeShape::Circle, NodeShape::Round, NodeShape::Rect, NodeShape::Rect]
        );
        let styles: Vec<(Stroke, bool)> = f.edges.iter().map(|e| (e.stroke, e.arrow)).collect();
        assert_eq!(
            styles,
            vec![
                (Stroke::Solid, true),
                (Stroke::Solid, true),
                (Stroke::Solid, true),
                (Stroke::Thick, true),
                (Stroke::Solid, false),
                (Stroke::Dashed, true)
            ]
        );
    }

    #[test]
    fn chains_ampersands_and_semicolons() {
        let f = flow("graph TD; A-->B-->C; A & B --> D");
        assert_eq!(labels(&f), vec!["A", "B", "C", "D"]);
        let pairs: Vec<(usize, usize)> = f.edges.iter().map(|e| (e.from, e.to)).collect();
        assert_eq!(pairs, vec![(0, 1), (1, 2), (0, 3), (1, 3)]);
    }

    #[test]
    fn edge_labels_inline_and_piped() {
        let f = flow("graph TD\nA -- go on --> B\nA -->|pipe<br>two| C\nA -. maybe .-> D");
        assert_eq!(f.edges[0].label, vec!["go on"]);
        assert_eq!(f.edges[1].label, vec!["pipe", "two"]);
        assert_eq!(f.edges[2].label, vec!["maybe"]);
        assert_eq!(f.edges[2].stroke, Stroke::Dashed);
    }

    #[test]
    fn styling_and_click_directives_are_ignored() {
        let f = flow("graph TD\nA-->B\nclick A \"javascript:alert(1)\"\nclick B callback\nstyle A fill:#f9f\nclassDef x fill:#f00\nclass A x\nlinkStyle 0 stroke:red\nsubgraph S [Group]\nC-->D\nend");
        assert_eq!(labels(&f), vec!["A", "B", "C", "D"]);
        assert_eq!(f.edges.len(), 2);
    }

    #[test]
    fn layout_directions_place_ranks_on_the_right_axis() {
        let centre = |scene: &Scene, label: &str| -> (f64, f64) {
            scene
                .shapes
                .iter()
                .find_map(|s| match s {
                    Shape::Text { x, y, lines, role: Role::Node, .. } if lines[0] == label => Some((*x, *y)),
                    _ => None,
                })
                .unwrap()
        };
        let td = render("graph TD\nA-->B").unwrap();
        assert!(centre(&td, "B").1 > centre(&td, "A").1);
        let bt = render("graph BT\nA-->B").unwrap();
        assert!(centre(&bt, "B").1 < centre(&bt, "A").1);
        let lr = render("graph LR\nA-->B").unwrap();
        assert!(centre(&lr, "B").0 > centre(&lr, "A").0);
        let rl = render("graph RL\nA-->B").unwrap();
        assert!(centre(&rl, "B").0 < centre(&rl, "A").0);
    }

    #[test]
    fn cycles_and_self_loops_lay_out() {
        let scene = render("graph TD\nA-->B\nB-->C\nC-->A\nC-->C").unwrap();
        let f = flow("graph TD\nA-->B\nB-->C\nC-->A");
        assert_eq!(ranks(&f), vec![0, 1, 2]);
        assert!(scene.shapes.iter().all(|s| match s {
            Shape::Line { points, .. } => points.iter().all(|(x, y)| x.is_finite() && y.is_finite()),
            _ => true,
        }));
    }

    #[test]
    fn valid_sequence_renders() {
        let src = "sequenceDiagram\n  participant U as User\n  actor S as Server\n  U->>S: request\n  S-->>U: response\n  U-xS: cancel\n  S-)U: async\n  U->U: think\n  Note right of S: logs it\n  Note over U,S: shared\n  loop every second\n  U->>+S: poll\n  end";
        let scene = render(src).unwrap();
        assert_eq!(scene.kind, "sequence");
        let texts = scene.texts();
        for want in ["User", "Server", "request", "response", "cancel", "async", "think", "logs it", "shared", "loop [every second]", "poll"] {
            assert!(texts.iter().any(|t| t == want), "missing {want}: {texts:?}");
        }
        let lines = statements(src);
        let seq = parse_sequence(&lines[1..]).unwrap();
        let kinds: Vec<(Stroke, bool)> = seq
            .items
            .iter()
            .filter_map(|i| match i {
                SeqItem::Message { stroke, head, .. } => Some((*stroke, *head)),
                _ => None,
            })
            .collect();
        assert_eq!(
            kinds,
            vec![
                (Stroke::Solid, true),
                (Stroke::Dashed, true),
                (Stroke::Solid, true),
                (Stroke::Solid, true),
                (Stroke::Solid, false),
                (Stroke::Solid, true)
            ]
        );
    }

    #[test]
    fn sequence_implicit_participants_and_autonumber() {
        let src = "sequenceDiagram\nautonumber\nAlice->>Bob: hi\nBob-->>Alice: yo";
        let lines = statements(src);
        let seq = parse_sequence(&lines[1..]).unwrap();
        assert_eq!(seq.participants.iter().map(|p| p.id.as_str()).collect::<Vec<_>>(), vec!["Alice", "Bob"]);
        let texts = render(src).unwrap().texts();
        assert!(texts.contains(&"1. hi".to_string()) && texts.contains(&"2. yo".to_string()), "{texts:?}");
    }

    #[test]
    fn invalid_syntax_is_a_parse_error_with_line() {
        let cases = [
            ("", 0),
            ("graph XY\nA-->B", 1),
            ("graph TD\nA-->", 2),
            ("graph TD\nA[unclosed --> B", 2),
            ("graph TD\nA -->|open B", 2),
            ("graph TD\nA ?? B", 2),
            ("sequenceDiagram\nAlice: no arrow", 2),
            ("sequenceDiagram\nNote somewhere: x", 2),
            ("classDiagram\nA <|-- B", 1),
            ("pie\n\"a\": 1", 1),
        ];
        for (src, line) in cases {
            let e = render(src).expect_err(src);
            assert_eq!(e.line, line, "{src:?} -> {e}");
            assert!(!e.message.is_empty());
        }
        assert!(render("classDiagram").unwrap_err().message.contains("not supported"));
    }

    #[test]
    fn untrusted_label_markup_stays_literal_text() {
        let src = "graph TD\nA[\"<img src=x onerror=alert(1)>\"] -->|<script>alert(2)</script>| B[<b>bold</b>]";
        let scene = render(src).unwrap();
        let texts = scene.texts();
        assert!(texts.contains(&"<img src=x onerror=alert(1)>".to_string()), "{texts:?}");
        assert!(texts.contains(&"<script>alert(2)</script>".to_string()), "{texts:?}");
        assert!(texts.contains(&"<b>bold</b>".to_string()), "{texts:?}");
        let seq = render("sequenceDiagram\nA->>B: <script>x</script>").unwrap();
        assert!(seq.texts().contains(&"<script>x</script>".to_string()));
    }

    #[test]
    fn br_variants_split_lines() {
        assert_eq!(label_lines("a<br>b<br/>c<br />d"), vec!["a", "b", "c", "d"]);
        assert_eq!(label_lines("\"quoted\""), vec!["quoted"]);
    }

    #[test]
    fn size_caps_refuse_huge_diagrams() {
        let big = format!("graph TD\n{}", "A-->B\n".repeat(4000));
        assert!(render(&big).unwrap_err().message.contains("bytes"));
        let many: String = (0..=MAX_NODES).map(|i| format!("N{i}\n")).collect();
        assert!(render(&format!("graph TD\n{many}")).unwrap_err().message.contains("nodes"));
    }

    #[test]
    fn comments_and_blank_lines_are_skipped() {
        let scene = render("%% top comment\ngraph TD\n\n%% inner\nA-->B\n").unwrap();
        assert_eq!(scene.kind, "flowchart");
    }

    #[test]
    fn arrow_ids_do_not_swallow_dashes() {
        let f = flow("graph TD\nnode-a-->node-b\nx.y --> z");
        assert_eq!(labels(&f), vec!["node-a", "node-b", "x.y", "z"]);
    }
}
