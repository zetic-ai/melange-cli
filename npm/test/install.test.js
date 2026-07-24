"use strict";

const assert = require("node:assert/strict");
const crypto = require("node:crypto");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");

const {
  archiveName,
  checksumFor,
  install,
  targetFor,
} = require("../scripts/install");

test("maps every released Node platform and architecture to its GoReleaser target", () => {
  assert.deepEqual(targetFor("darwin", "arm64"), {
    os: "darwin",
    arch: "arm64",
    extension: "tar.gz",
    executable: "melange",
  });
  assert.deepEqual(targetFor("linux", "x64"), {
    os: "linux",
    arch: "amd64",
    extension: "tar.gz",
    executable: "melange",
  });
  assert.deepEqual(targetFor("win32", "arm64"), {
    os: "windows",
    arch: "arm64",
    extension: "zip",
    executable: "melange.exe",
  });
});

test("rejects platforms for which the release has no binary", () => {
  assert.throws(
    () => targetFor("freebsd", "x64"),
    /Unsupported platform: freebsd-x64/,
  );
  assert.throws(
    () => targetFor("darwin", "ia32"),
    /Unsupported platform: darwin-ia32/,
  );
});

test("builds an archive name from the npm version and release target", () => {
  assert.equal(
    archiveName("0.1.0", targetFor("darwin", "arm64")),
    "melange_0.1.0_darwin_arm64.tar.gz",
  );
  assert.equal(
    archiveName("0.1.0", targetFor("win32", "x64")),
    "melange_0.1.0_windows_amd64.zip",
  );
});

test("reads only an exact artifact checksum", () => {
  const checksums = [
    `${"a".repeat(64)}  melange_0.1.0_darwin_arm64.tar.gz`,
    `${"b".repeat(64)}  melange_0.1.0_linux_arm64.tar.gz`,
    "",
  ].join("\n");

  assert.equal(
    checksumFor(checksums, "melange_0.1.0_darwin_arm64.tar.gz"),
    "a".repeat(64),
  );
  assert.throws(
    () => checksumFor(checksums, "melange_0.1.0_windows_arm64.zip"),
    /No checksum found/,
  );
});

test("installs only after the downloaded archive matches its published checksum", async (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "melange-npm-test-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));

  const archive = Buffer.from("release archive");
  const digest = crypto.createHash("sha256").update(archive).digest("hex");
  const checksums = `${digest}  melange_0.1.0_darwin_arm64.tar.gz\n`;
  let extracted = false;

  await install({
    packageRoot: root,
    packageVersion: "0.1.0",
    platform: "darwin",
    architecture: "arm64",
    checksums,
    download: async (_url, destination) => {
      fs.writeFileSync(destination, archive);
    },
    extract: (_archivePath, _target, destination) => {
      extracted = true;
      fs.mkdirSync(destination, { recursive: true });
      fs.writeFileSync(path.join(destination, "melange"), "binary");
    },
  });

  assert.equal(extracted, true);
  assert.equal(
    fs.readFileSync(path.join(root, "vendor", "darwin-arm64", "melange"), "utf8"),
    "binary",
  );
});

test("does not extract an archive whose checksum does not match", async (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "melange-npm-test-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  let extracted = false;

  await assert.rejects(
    install({
      packageRoot: root,
      packageVersion: "0.1.0",
      platform: "linux",
      architecture: "x64",
      checksums: `${"0".repeat(64)}  melange_0.1.0_linux_amd64.tar.gz\n`,
      download: async (_url, destination) => {
        fs.writeFileSync(destination, "tampered");
      },
      extract: () => {
        extracted = true;
      },
    }),
    /Checksum verification failed/,
  );

  assert.equal(extracted, false);
});
