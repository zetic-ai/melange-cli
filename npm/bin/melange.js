#!/usr/bin/env node
"use strict";

const childProcess = require("node:child_process");
const path = require("node:path");
const { targetFor } = require("../scripts/install");

let target;
try {
  target = targetFor(process.platform, process.arch);
} catch (error) {
  process.stderr.write(`melange: ${error.message}\n`);
  process.exit(1);
}

const binary = path.join(
  __dirname,
  "..",
  "vendor",
  `${process.platform}-${process.arch}`,
  target.executable,
);
const result = childProcess.spawnSync(binary, process.argv.slice(2), {
  env: process.env,
  stdio: "inherit",
});

if (result.error) {
  process.stderr.write(
    `melange: binary is unavailable; reinstall @zetic-ai/melange-cli (${result.error.message})\n`,
  );
  process.exit(1);
}
if (result.signal) {
  process.kill(process.pid, result.signal);
}
process.exit(result.status ?? 1);
