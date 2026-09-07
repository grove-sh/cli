#!/usr/bin/env node
"use strict";

// A global install links the bin of the package you named and not the bins of
// its dependencies, so nothing can point `grove` straight at the binary and
// leave node out. This process therefore lives as long as grove does, which
// makes signals its problem rather than a detail: see below.

const { spawn } = require("node:child_process");
const os = require("node:os");
const path = require("node:path");

// Keyed by process.platform and os.arch(), not by Go's names for them. An arm64
// Mac running node under Rosetta reports x64 and gets the x64 build, correctly:
// that is what such a node can run.
const BUILDS = {
  darwin: { arm64: "darwin-arm64", x64: "darwin-x64" },
  linux: { arm64: "linux-arm64", x64: "linux-x64" },
};

function fail(message) {
  process.stderr.write("grove: " + message + "\n");
  process.exit(1);
}

function locate() {
  // For the rehearsal, which has binaries but no published packages to resolve
  // them from.
  const override = process.env.GROVE_BINARY_OVERRIDE;
  if (override) return override;

  const platform = process.platform;
  const arch = os.arch();
  const build = (BUILDS[platform] || {})[arch];
  if (!build) {
    fail(
      `there is no build for ${platform}-${arch}. Supported: ` +
        Object.keys(BUILDS)
          .flatMap((p) => Object.keys(BUILDS[p]).map((a) => `${p}-${a}`))
          .sort()
          .join(", ")
    );
  }

  const pkg = `@grove-sh/cli-${build}`;
  let manifest;
  try {
    manifest = require.resolve(`${pkg}/package.json`);
  } catch {
    // npm does not report an optional dependency it could not fetch, so this
    // is the first anyone hears of it. Blaming --no-optional was wrong often
    // enough to be worth not saying: an install run in the first minutes after
    // a release does this too, when the wrapper has reached a mirror ahead of
    // the platform package it depends on.
    fail(
      `${pkg} is not installed, so there is no binary to run.\n` +
        "npm counts it as optional, so an install that skipped optional\n" +
        "dependencies leaves it out, and so does one run before a new release\n" +
        "has reached every mirror. Installing again fixes both:\n" +
        "  npm install -g @grove-sh/cli"
    );
  }
  return path.join(path.dirname(manifest), "bin", "grove");
}

const binary = locate();

// Inherited stdio, so grove sees the real terminal: it colours only for one,
// and `grove exec` hands the terminal to the command it runs.
const child = spawn(binary, process.argv.slice(2), { stdio: "inherit" });

// grove decides what its signals mean: exec relays the first to the command it
// runs and kills on the second, none of which works if this process dies to a
// default action first. A terminal's Ctrl-C reaches the child through the
// process group anyway; one sent to this pid alone only arrives if forwarded.
const forwarded = ["SIGINT", "SIGTERM", "SIGHUP"];
const forwarders = new Map();
for (const signal of forwarded) {
  const forward = () => {
    if (child.exitCode === null && child.signalCode === null) child.kill(signal);
  };
  process.on(signal, forward);
  forwarders.set(signal, forward);
}
const stopForwarding = () => {
  for (const [signal, forward] of forwarders) process.removeListener(signal, forward);
};

child.on("error", (err) => {
  stopForwarding();
  if (err.code === "EACCES") {
    // The classic packaging failure, and it looks like grove's fault.
    fail(`${binary} is not executable. This is a packaging bug: please report it.`);
  }
  if (err.code === "ENOENT") {
    fail(`${binary} is missing. This is a packaging bug: please report it.`);
  }
  fail(`could not run ${binary}: ${err.message}`);
});

child.on("exit", (code, signal) => {
  stopForwarding();
  if (signal !== null) {
    // So the shell reports the conventional 128+n rather than a plain code
    // that loses how it ended.
    process.kill(process.pid, signal);
    setInterval(() => {}, 1000); // hold the loop open until the signal lands
    return;
  }
  process.exit(code === null ? 1 : code);
});
