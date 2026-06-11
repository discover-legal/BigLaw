# bench.ps1 — one-command Harvey LAB benchmark runner for Windows.
#
#   .\bench.ps1 -List                                       # browse the dataset
#   .\bench.ps1 -Task corporate-ma/review-data-room-red-flag-review
#   .\bench.ps1 -Task <task> -GatePolicy reject -ModelDir biglaw-reject
#
# Does everything the README's Prerequisites section lists:
#   1. clones harvey-labs (dataset + eval harness) if missing
#   2. installs the driver's Python dependencies if missing
#   3. starts a native BigLaw backend if none is reachable (logs to data/)
#   4. runs the driver, passing any extra arguments straight through
# The driver prints the `uv run python -m evaluation.run_eval ...` command
# to score the run when it finishes.

[CmdletBinding()]
param(
    [string]$LabsDir = (Join-Path $HOME "harvey-labs"),
    [string]$Api = "http://localhost:3101",
    [string]$Task,
    [ValidateSet("approve", "reject")][string]$GatePolicy,
    [string]$ModelDir,
    [switch]$List,
    # Anything else (e.g. --workflow tabulate --split-mode per-task) is handed
    # to run.py verbatim.
    [Parameter(ValueFromRemainingArguments = $true)][string[]]$DriverArgs
)

$ErrorActionPreference = "Stop"
$benchDir = $PSScriptRoot
$repoRoot = (Resolve-Path (Join-Path $benchDir "..\..")).Path

function Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }

if (-not $List -and -not $Task) {
    Write-Host "No -Task given. Browse the dataset with:  .\bench.ps1 -List" -ForegroundColor Yellow
    Write-Host "Then run one task:                        .\bench.ps1 -Task <area>/<task>" -ForegroundColor Yellow
    Write-Host "Heads-up: every LAB task is a full DyTopo run with Opus debate + synthesis — watch /cost/summary." -ForegroundColor Yellow
    exit 1
}

# ── 1. Dataset checkout ───────────────────────────────────────────────────────
if (-not (Test-Path (Join-Path $LabsDir "tasks"))) {
    Step "harvey-labs not found at $LabsDir — cloning"
    git clone https://github.com/harveyai/harvey-labs $LabsDir
} else {
    Step "harvey-labs dataset: $LabsDir"
}

# ── 2. Python + driver dependencies ───────────────────────────────────────────
$py = Get-Command python -ErrorAction SilentlyContinue
if (-not $py) { throw "python not found on PATH — install Python 3.11+ first" }
$pyVersion = & python -c "import sys; print('{}.{}'.format(*sys.version_info[:2]))"
if ([version]$pyVersion -lt [version]"3.11") {
    throw "Python 3.11+ required (found $pyVersion)"
}
& python -c "import requests, fitz, docx, openpyxl, pptx" 2>$null
if ($LASTEXITCODE -ne 0) {
    Step "installing driver dependencies (pip install -r requirements.txt)"
    & python -m pip install -q -r (Join-Path $benchDir "requirements.txt")
    if ($LASTEXITCODE -ne 0) { throw "pip install failed" }
} else {
    Step "driver dependencies present"
}

# ── 3. Backend ────────────────────────────────────────────────────────────────
function Test-Backend {
    try { (Invoke-RestMethod -Uri "$Api/health" -TimeoutSec 3) -ne $null } catch { $false }
}

if (Test-Backend) {
    Step "BigLaw backend already running at $Api"
} else {
    if (-not $env:ANTHROPIC_API_KEY -and -not (Test-Path (Join-Path $repoRoot ".env"))) {
        Write-Warning "No ANTHROPIC_API_KEY in the environment and no .env at the repo root — the backend will start but every model call will fail."
    }
    Step "no backend at $Api — starting native backend (go run ./biglaw-go/cmd/biglaw)"
    $logDir = Join-Path $repoRoot "data"
    New-Item -ItemType Directory -Force $logDir | Out-Null
    Start-Process -FilePath "go" -ArgumentList "run", "./biglaw-go/cmd/biglaw" `
        -WorkingDirectory $repoRoot -WindowStyle Hidden `
        -RedirectStandardOutput (Join-Path $logDir "bench-backend.log") `
        -RedirectStandardError (Join-Path $logDir "bench-backend.err.log")
    Step "waiting for $Api/health (first start compiles — up to ~2 min)"
    $deadline = (Get-Date).AddSeconds(150)
    while (-not (Test-Backend)) {
        if ((Get-Date) -gt $deadline) {
            throw "backend did not become healthy; see data\bench-backend*.log"
        }
        Start-Sleep -Seconds 3
    }
    Step "backend is up (it keeps running after this script — stop it from Task Manager or with Stop-Process -Name biglaw)"
}

# ── 4. Run the driver ─────────────────────────────────────────────────────────
$args2 = @("run.py", "--labs-dir", $LabsDir, "--api", $Api)
if ($List) { $args2 += "--list" }
if ($Task) { $args2 += @("--task", $Task) }
if ($GatePolicy) { $args2 += @("--gate-policy", $GatePolicy) }
if ($ModelDir) { $args2 += @("--model-dir", $ModelDir) }
if ($DriverArgs) { $args2 += $DriverArgs }

Step "python $($args2 -join ' ')"
Push-Location $benchDir
try { & python @args2 } finally { Pop-Location }
