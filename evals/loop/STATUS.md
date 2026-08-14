# Campaign status

**Now:** i1 ACCEPT (21/25). **i2 TB DEV running** — map `/app` file-tool paths onto the host bind-mount.

Honest DEV so far:
- SWE 44/60, 45/60 (pair 3 pending)
- TB 18/25, 16/25, 19/25

TB always-fail (3/3): `build-cython-ext`, `financial-document-processor`, `openssl-selfsigned-cert`, `portfolio-optimization`. Root cause: host `network.allow` blocks `python3`/`openssl`/`git`, and there is no `/app` on the host. Iteration 1 routes TB bash into a live bind-mounted task image.

Watch:

```bash
tail -f evals/loop/results/baseline.log
python3 evals/loop/spend.py
pgrep -af 'continue_baseline|run_parallel|strike eval'
```

Stop the campaign:

```bash
kill 1030533   # continue_baseline, if still up
pkill -f 'evals/loop/run_parallel.py'
# do not pkill -f strike — that has killed this agent before
```

Do not inspect HOLDOUT instance trajectories. HOLDOUT lists exist but are not launched until iterations 5/10/15/20.
