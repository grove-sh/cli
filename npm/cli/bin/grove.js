#!/usr/bin/env node
"use strict";

// The npm package for a Go program is a wrapper around one binary per
// platform. npm installs exactly one of them, picked by the "os" and "cpu"
// fields on the packages listed in optionalDependencies, and this file finds
// the one that landed and hands over to it.
//
// A global install links the bin of the package you named and not the bins of
// its dependencies, so there is no arrangement that points the `grove` command
// straight at the binary and leaves node out. This process therefore lives for
// as long as grove does, which makes signals its problem rather than a detail:
// see below.

const { spawn } = require("node:child_process");
const os = require("node:os");
const path = require("node:path");

// Keyed by process.platform and os.arch(), which is what the package names use
// and not what Go calls them. An arm64 Mac running node under Rosetta reports
// x64 and gets the x64 build, which is correct: it is what that node can run.
const BUILDS = {
  darwin: { arm64: "darwin-arm64", x64: "darwin-x64" },
  linux: { arm64: "linux-arm64", x64: "linux-x64" },
};

function fail(message) {
  process.stderr.write("grove: " + message + "\n");
  process.exit(1);
}

function locate() {
  // Set by the rehearsal, which has binaries but no published packages to
  // resolve them from. Nothing else should need it.
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
    // An optional dependency that did not install is not an error npm reports,
    // so this is the first anyone hears of it. The two ways it happens are an
    // install run with --no-optional and a cache that served a partial tree.
    fail(
      `${pkg} is not installed, so there is no binary to run.\n` +
        "Reinstall without --no-optional: npm install -g @grove-sh/cli"
    );
  }
  return path.join(path.dirname(manifest), "bin", "grove");
}

const binary = locate();

// stdio is inherited so grove sees the real terminal: it colours its output
// only for one, and `grove exec` hands the terminal to the command it runs.
const child = spawn(binary, process.argv.slice(2), { stdio: "inherit" });

// grove decides what its signals mean. `grove exec` relays the first one to
// the command it is running and kills on the second, and none of that works if
// this process takes a signal's default action and dies while grove is still
// running. A terminal's Ctrl-C reaches the child on its own, since it goes to
// the whole process group; a signal sent to this pid alone, as a supervisor or
// a `kill` would send it, only reaches grove if it is forwarded.
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
    // The classic packaging failure: the file shipped without its executable
    // bit, so every install is broken in a way that looks like grove's fault.
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
    // Die the same way the child did, so the shell reports the conventional
    // 128+n rather than a plain exit code that loses how it ended.
    process.kill(process.pid, signal);
    setInterval(() => {}, 1000); // hold the loop open until the signal lands
    return;
  }
  process.exit(code === null ? 1 : code);
});
