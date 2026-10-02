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
// Everything after the `--` is forwarded verbatim, so the Go installer's own
// subcommands keep working:
//   llm-gw-installer doctor
//   llm-gw-installer install --dir ~/llm-gateway --mode lite
//   llm-gw-installer version

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

function run(bin, args, shell) {
  const res = spawnSync(bin, args, { stdio: 'inherit', shell: !!shell });
  if (res.error) {
    console.error('[llm-gw-installer] failed to run ' + bin + ': ' + res.error.message);
    return 127;
  }
  return res.status === null ? 1 : res.status;
}

function runShell(cmd) {
  const shell = isWindows() ? process.env.ComSpec || 'cmd.exe' : '/bin/sh';
  return run(shell, ['-c', cmd], true);
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

function main() {
  const argv = process.argv.slice(2);

  if (argv.includes('--help') || argv.includes('-h') || argv[0] === 'help') {
    usage();
    return 0;
  }

  const local = findLocalBinary();
  if (local) {
    return run(local, argv, false);
  }

  // Nothing local: hand over to the official one-liner for this platform.
  const url = maintainBase() + '/distribution/install-scripts/install';
  console.error('[llm-gw-installer] no local installer binary found; using the official installer for ' + process.platform);
  console.error('[llm-gw-installer] ' + url);

  if (isWindows()) {
    // `irm … | iex` is the PowerShell equivalent of `curl … | bash`; it runs
    // the same served script, which then asks for the scale and dispatches.
    const ps = 'powershell -NoLogo -NoProfile -ExecutionPolicy Bypass -Command ' +
      '"$s = Invoke-RestMethod -Uri \'' + url + '\'; Invoke-Expression $s"';
    return runShell(ps);
  }
  return runShell('curl -fsSL "' + url + '" | bash');
}

process.exit(main());
