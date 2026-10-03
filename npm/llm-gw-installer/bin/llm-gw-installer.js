#!/usr/bin/env node
'use strict';

// llm-gw-installer — npm entry point for llm-gateway-go.
//
// Why this exists: on Windows the lowest-friction install is `npm install -g`
// because Node is the toolchain most machines already have, and the shell
// one-liner (`curl … | bash` / `irm … | iex`) needs a shell the user may not
// have. This launcher gives `npm i -g @kaixuan/llm-gw-installer` the same
// two-scale experience the other channels have.
//
// It deliberately does NOT re-implement the download/verify logic. The
// release catalog, the download ticket and the mandatory sha256 check all
// live in the maintain API and are already covered end-to-end by
// scripts/tests/install-modes-test.sh. Re-implementing them in JS would be a
// second source of truth that could silently drift away from the sha256 hard
// gate. Instead this picks the right *already tested* path for the platform:
//
//   1. an llm-gw-installer binary that is already on this machine
//      (so `npm i -g` composes with `go install` / a release bundle)
//   2. otherwise the official one-liner for this platform
//        unix    curl -fsSL "$MAINTAIN_BASE/distribution/install-scripts/install" | bash
//        windows irm "$MAINTAIN_BASE/distribution/install-scripts/install" | iex
//
// doctor / version / --help are answered by this launcher itself (see main())
// — they are NOT forwarded, because the resolved binary may not exist yet and
// these must work right after `npm i -g`. Every other invocation is forwarded
// verbatim to the resolved installer binary, so the Go installer's own
// subcommands keep working:
//   llm-gw-installer install --dir ~/llm-gateway --mode lite
//   llm-gw-installer upgrade --action check

const { spawnSync } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');

const DEFAULT_MAINTAIN_BASE = 'https://llmgateway.internal.example.com/maintain-api';

function maintainBase() {
  return process.env.MAINTAIN_BASE || DEFAULT_MAINTAIN_BASE;
}

function exeSuffix() {
  return process.platform === 'win32' ? '.exe' : '';
}

function isWindows() {
  return process.platform === 'win32';
}

// A binary that already exists wins over fetching anything, so repeated
// invocations stay offline and fast.
function findLocalBinary() {
  const name = 'llm-gw-installer' + exeSuffix();
  const arch = process.arch === 'arm64' ? 'arm64' : 'amd64';
  const candidates = [
    path.join(__dirname, name),
    path.join(__dirname, '..', '..', '..', 'llm-gw-installer-' + process.platform + '-' + arch + exeSuffix()),
    path.join(process.env.LLM_GATEWAY_HOME || path.join(os.homedir(), 'llm-gateway'), 'bin', name),
  ];
  for (const c of candidates) {
    try {
      if (fs.existsSync(c)) return c;
    } catch (_) {
      /* unreadable candidate: keep looking */
    }
  }
  return null;
}

function run(bin, args) {
  const res = spawnSync(bin, args, { stdio: 'inherit' });
  if (res.error) {
    console.error('[llm-gw-installer] failed to run ' + bin + ': ' + res.error.message);
    return 127;
  }
  return res.status === null ? 1 : res.status;
}

function usage() {
  console.log([
    'llm-gw-installer — llm-gateway-go installer',
    '',
    'Usage:',
    '  llm-gw-installer                        interactive install (asks lite / full)',
    '  llm-gw-installer doctor                 environment report',
    '  llm-gw-installer version                version + how to get updates',
    '  llm-gw-installer install --dir <path> --mode lite|full',
    '  llm-gw-installer uninstall [--purge]',
    '',
    'Environment:',
    '  MAINTAIN_BASE     release entry (default ' + DEFAULT_MAINTAIN_BASE + ')',
    '  LLM_GATEWAY_HOME  install dir (default ~/llm-gateway)',
  ].join('\n'));
}

// The served entry is a shell script, so argv has to be forwarded in whatever
// dialect the target shell speaks. Two shapes:
//
//   unix    curl -fsSL "$URL" | bash -s -- <args>     (args after `--`)
//   windows irm "$URL" | iex                          (iex takes no args)
//
// On Windows the arguments are therefore translated into the environment
// variables the entry already reads (MODE / CHANNEL / NO_INTERACTIVE /
// DRY_RUN). Dropping argv on this path meant `npm i -g … -- --mode lite`
// silently installed the interactive default instead, which is exactly the
// class of bug this launcher is supposed to remove.
function shellQuote(s) {
  return "'" + String(s).replace(/'/g, "'\\''") + "'";
}

function unixFallback(url, argv) {
  const tail = argv.length ? ' -s -- ' + argv.map(shellQuote).join(' ') : '';
  return 'curl -fsSL ' + shellQuote(url) + ' | bash' + tail;
}

const PS_FLAG_TO_ENV = {
  '--mode': 'MODE',
  '--channel': 'CHANNEL',
};

// Splits argv into what the Windows fallback can honour (as $env: assignments)
// and what it cannot (reported by Node itself). One classifier, so the script
// text and the warning can never disagree about the same argument.
function windowsClassifyArgs(argv) {
  const env = [];
  const ignored = [];
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (PS_FLAG_TO_ENV[a] && argv[i + 1] !== undefined) {
      env.push(`$env:${PS_FLAG_TO_ENV[a]}=${shellQuoteForPS(argv[++i])}`);
    } else if (a === '--yes' || a === '-y') {
      env.push("$env:NO_INTERACTIVE='1'");
    } else if (a === '--dry-run') {
      env.push("$env:DRY_RUN='1'");
    } else {
      ignored.push(a);
    }
  }
  return { env, ignored };
}

// Returns the **PowerShell source text** only — no shell wrapper. The caller
// hands it to powershell.exe as a single argv element (see windowsCommand), so
// this text must stay valid PowerShell and must not be escaped for a POSIX
// shell. It previously ended in a POSIX shellQuote(), which was silently wrong
// the moment it stopped being read by /bin/sh.
//
// The generated source is deliberately **pure ASCII**. A -Command argument goes
// through the console code page on its way into powershell.exe, and non-ASCII in
// there gets mangled badly enough to unbalance the quoting — a Chinese
// Write-Warning message turned into `The string is missing the terminator: '`.
// The "these args are ignored" notice is therefore emitted by Node, not from
// inside the script.
function windowsFallback(url, argv) {
  const { env } = windowsClassifyArgs(argv);
  // The `if ($s)` guard matters: when the entry is not published yet the fetch
  // yields $null, and `Invoke-Expression $null` then fails with a misleading
  // "Cannot bind argument to parameter 'Command'" instead of saying the fetch
  // came back empty.
  return (env.length ? env.join('; ') + '; ' : '') +
    `$s = Invoke-RestMethod -Uri ${shellQuoteForPS(url)}; ` +
    'if ($s) { Invoke-Expression $s } else { Write-Error "llm-gw-installer: empty response from the release entry"; exit 1 }';
}

function shellQuoteForPS(s) {
  return "'" + String(s).replace(/'/g, "''") + "'";
}

// The spawn shape for the Windows fallback.
//
// This deliberately does NOT go through cmd.exe. Two bugs lived in that detour,
// both invisible to a test that only asserts on the generated *string*:
//
//  1. `cmd.exe -c "<cmd>"` — cmd's switch is `/c`, not `-c`. Given `-c` and no
//     `/c`, cmd starts an **interactive** cmd instead of running anything: it
//     prints the banner and a prompt, executes nothing, and still exits 0. So
//     `npm i -g @kaixuan/llm-gw-installer` then `llm-gw-installer doctor` opened
//     a stray console window and reported success.
//  2. The string handed to cmd wrapped the PowerShell part in POSIX quoting
//     (`'\''`), which cmd passes through verbatim, so PowerShell died with a
//     ParserError on the `'\''` sequences.
//
// Passing the script as one argv element to powershell.exe avoids both: there is
// no shell to re-quote, and Node quotes for the Windows C runtime.
function windowsCommand(url, argv) {
  return {
    bin: 'powershell.exe',
    args: ['-NoLogo', '-NoProfile', '-ExecutionPolicy', 'Bypass', '-Command', windowsFallback(url, argv)],
  };
}

// The one place that decides *how* a fallback is launched, for both platforms.
//
// This exists as a named seam on purpose. The original bug (cmd.exe -c opening
// an interactive shell) lived in main()'s dispatch, while the helper it called
// was perfectly fine — so a test asserting only on the helper stayed green while
// every real Windows invocation was broken. main() now routes through here, so
// what the gate asserts on is the path users actually take. `platform` is a
// parameter so the Windows branch is testable from any host.
function fallbackPlan(url, argv, platform) {
  const p = platform || process.platform;
  if (p === 'win32') {
    const c = windowsCommand(url, argv);
    c.ignored = windowsClassifyArgs(argv).ignored;
    return c;
  }
  return { bin: '/bin/sh', args: ['-c', unixFallback(url, argv)] };
}

// doctor / version are answered by the launcher itself, for the same reason the
// other entry points do it: they are most useful exactly when nothing is
// installed yet, and the fallback cannot carry them — `irm | iex` takes no
// positional arguments, so there is nowhere to put "doctor". On Windows they
// would otherwise degrade into "arg ignored" + a real install attempt.
function localDoctor() {
  const local = findLocalBinary();
  const home = process.env.LLM_GATEWAY_HOME || path.join(os.homedir(), 'llm-gateway');
  console.log([
    'llm-gw-installer doctor',
    '',
    '  platform        : ' + process.platform + '/' + process.arch,
    '  node            : ' + process.version,
    '  install dir     : ' + home,
    '  local binary    : ' + (local || '(none — the official installer will be used)'),
    '  release entry   : ' + maintainBase(),
    '',
    'Install:',
    '  lite (host binary + SQLite)   npm i -g @kaixuan/llm-gw-installer -- --mode lite',
    '  full (Docker + PG + Redis)    npm i -g @kaixuan/llm-gw-installer -- --mode full',
  ].join('\n'));
  return 0;
}

function localVersion() {
  const local = findLocalBinary();
  console.log([
    'llm-gw-installer ' + require('../package.json').version,
    '  launcher       : ' + require('../package.json').version,
    '  local binary   : ' + (local || '(none)'),
    '  release entry  : ' + maintainBase(),
    '',
    'To get the latest gateway release:',
    '  node_modules/.bin/llm-gw-installer --mode lite     (host binary + SQLite)',
    '  node_modules/.bin/llm-gw-installer --mode full      (Docker + PostgreSQL + Redis)',
  ].join('\n'));
  return 0;
}

function main() {
  const argv = process.argv.slice(2);

  if (argv.includes('--help') || argv.includes('-h') || argv[0] === 'help') {
    usage();
    return 0;
  }
  if (argv[0] === 'doctor') {
    return localDoctor();
  }
  if (argv[0] === 'version') {
    return localVersion();
  }

  const local = findLocalBinary();
  if (local) {
    return run(local, argv);
  }

  // Nothing local: hand over to the official one-liner for this platform.
  const url = maintainBase() + '/distribution/install-scripts/install';
  console.error('[llm-gw-installer] no local installer binary found; using the official installer for ' + process.platform);
  console.error('[llm-gw-installer] ' + url);

  const plan = fallbackPlan(url, argv);
  if (isWindows() && plan.ignored && plan.ignored.length) {
    // Printed here, not from inside the PowerShell script: a -Command argument
    // is re-encoded through the console code page, and non-ASCII in it corrupts
    // the quoting (see windowsFallback).
    console.error('[llm-gw-installer] these arguments have no effect on the Windows fallback: ' +
      plan.ignored.join(' '));
  }
  return run(plan.bin, plan.args);
}

// Exported so scripts/checks/install-entrypoints-test.sh can assert on the
// generated command without actually fetching or executing anything.
module.exports = {
  unixFallback, windowsFallback, windowsCommand, windowsClassifyArgs,
  fallbackPlan, findLocalBinary, localDoctor, localVersion, main,
};

if (require.main === module) {
  process.exit(main());
}
