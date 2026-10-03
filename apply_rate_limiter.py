#!/usr/bin/env python3
"""
Hooks the database-backed rate limiter into your main.go.

Usage (keep this script, ratelimit_db.go, ratelimit_db_test.go and
rate_limits.sql together):

    python3 apply_rate_limiter.py /path/to/your/project --dry-run
    python3 apply_rate_limiter.py /path/to/your/project

It makes 6 small edits to main.go (each must match exactly once or nothing is
written), backs it up to main.go.rl.bak, copies ratelimit_db.go and
ratelimit_db_test.go into the project, and adds the rate_limits migration with
the next free number. Matching ignores indentation.
"""
import re
import shutil
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
args = [a for a in sys.argv[1:] if not a.startswith("--")]
DRY = "--dry-run" in sys.argv
PROJECT = Path(args[0] if args else ".").resolve()
MAIN = PROJECT / "main.go"

if not MAIN.exists():
    sys.exit(f"main.go not found in {PROJECT}. Pass your project folder as the first argument.")

src = MAIN.read_text(encoding="utf-8")

if "sharedRetryAfter(" in src:
    sys.exit("main.go already has the shared rate limiter hooks. Nothing to do.")


def block_pattern(anchor: str) -> re.Pattern:
    lines = [l.strip() for l in anchor.strip("\n").splitlines()]
    body = r"[ \t]*\r?\n[ \t]*".join(re.escape(l) for l in lines)
    return re.compile(r"^[ \t]*" + body, re.M)


failures = []
count = 0


def edit(name: str, anchor: str, replacement: str) -> None:
    global src, count
    pat = block_pattern(anchor)
    found = list(pat.finditer(src))
    if len(found) != 1:
        failures.append(f"{name}: expected 1 match, found {len(found)}")
        return
    src = pat.sub(lambda m: replacement.strip("\n"), src, count=1)
    count += 1


edit(
    "rateLimiter struct: shared bucket field",
    """
type rateLimiter struct {
max int
""",
    """
type rateLimiter struct {
	max int

	// shared names this limiter's rows in the rate_limits table. Empty means
	// in-memory only. Set by enableSharedRateLimits when the table is usable.
	shared string
""",
)

edit(
    "retryAfter hook",
    "func (l *rateLimiter) retryAfter(key string) time.Duration {",
    """
func (l *rateLimiter) retryAfter(key string) time.Duration {
	if d, ok := l.sharedRetryAfter(key); ok {
		return d
	}
""",
)

edit(
    "fail hook",
    "func (l *rateLimiter) fail(key string) time.Duration {",
    """
func (l *rateLimiter) fail(key string) time.Duration {
	if d, ok := l.sharedFail(key); ok {
		return d
	}
""",
)

edit(
    "reset hook",
    "func (l *rateLimiter) reset(key string) {",
    """
func (l *rateLimiter) reset(key string) {
	l.sharedReset(key)
""",
)

edit(
    "sweep hook",
    "func (l *rateLimiter) sweep() {",
    """
func (l *rateLimiter) sweep() {
	l.sharedSweep()
""",
)

edit(
    "enable shared limits at startup",
    """
initSessionStore()
startRateLimiterSweeper()
""",
    """
	initSessionStore()
	enableSharedRateLimits() // before the sweeper starts, so it sees the final setup
	startRateLimiterSweeper()
""",
)

if failures:
    print("Nothing was written. These edits did not match your main.go exactly once:\n")
    for f in failures:
        print("  -", f)
    print("\nSend me the current main.go and I will adjust.")
    sys.exit(1)

print(f"All {count} edits matched.")
if DRY:
    print("--dry-run: no files changed.")
    sys.exit(0)

shutil.copy2(MAIN, PROJECT / "main.go.rl.bak")
MAIN.write_text(src, encoding="utf-8")
print("main.go updated (backup: main.go.rl.bak)")

for name in ("ratelimit_db.go", "ratelimit_db_test.go"):
    s, d = HERE / name, PROJECT / name
    if not s.exists():
        print(f"WARNING: {name} not found next to this script; copy it into your project yourself.")
    elif d.exists() and d.read_bytes() == s.read_bytes():
        print(f"{name} already in place")
    else:
        shutil.copy2(s, d)
        print(f"{name} added")

SQL_SRC = HERE / "rate_limits.sql"
mig_dir = PROJECT / "migrations"
if mig_dir.is_dir():
    existing = sorted(p.name for p in mig_dir.glob("*.sql"))
    if any("rate_limits" in n for n in existing):
        print("rate_limits migration already present")
    elif SQL_SRC.exists():
        nums = [(int(m.group(1)), len(m.group(1))) for n in existing if (m := re.match(r"(\d+)_", n))]
        if nums:
            top, width = max(nums)
            name = f"{top + 1:0{width}d}_rate_limits.sql"
        else:
            name = "001_rate_limits.sql"
        shutil.copy2(SQL_SRC, mig_dir / name)
        print(f"migration added: migrations/{name}  (check it follows your naming and that applyMigrations picks it up)")
else:
    print("NOTE: no migrations/ folder found. Run rate_limits.sql against your database or add it to your migrations yourself.")

if shutil.which("gofmt"):
    subprocess.run(["gofmt", "-w", str(MAIN)] + [str(PROJECT / n) for n in ("ratelimit_db.go", "ratelimit_db_test.go") if (PROJECT / n).exists()])
    print("gofmt applied")
if shutil.which("go"):
    r = subprocess.run(["go", "build", "./..."], cwd=PROJECT, capture_output=True, text=True)
    print("go build: OK" if r.returncode == 0 else "go build FAILED:\n" + r.stderr)
else:
    print("Go not found on PATH; run `go build ./... && go vet ./...` yourself.")
