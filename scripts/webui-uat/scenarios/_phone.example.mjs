// Copy to <stream>.mjs (drop the leading underscore) and change the slug.
// make webui-uat runs every such file after the cockpit pass.
// make webui-uat SUITE=<stream> runs only that file.
// Files starting with _ are not loaded.
export const scenario = (ctx) => ctx.phone("stream-empty");
