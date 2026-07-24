#!/usr/bin/env node
"use strict";

const childProcess = require("node:child_process");
const crypto = require("node:crypto");
const fs = require("node:fs");
const https = require("node:https");
const os = require("node:os");
const path = require("node:path");

const RELEASE_BASE_URL =
  "https://github.com/zetic-ai/melange-cli/releases/download";

function targetFor(platform, architecture) {
  const platformMap = {
    darwin: { os: "darwin", extension: "tar.gz" },
    linux: { os: "linux", extension: "tar.gz" },
    win32: { os: "windows", extension: "zip" },
  };
  const architectureMap = { x64: "amd64", arm64: "arm64" };
  const mappedPlatform = platformMap[platform];
  const mappedArchitecture = architectureMap[architecture];

  if (!mappedPlatform || !mappedArchitecture) {
    throw new Error(`Unsupported platform: ${platform}-${architecture}`);
  }

  return {
    os: mappedPlatform.os,
    arch: mappedArchitecture,
    extension: mappedPlatform.extension,
    executable: platform === "win32" ? "melange.exe" : "melange",
  };
}

function archiveName(version, target) {
  return `melange_${version}_${target.os}_${target.arch}.${target.extension}`;
}

function checksumFor(contents, artifactName) {
  for (const line of contents.split(/\r?\n/)) {
    const match = line.match(/^([a-fA-F0-9]{64})\s{1,2}(.+)$/);
    if (match && match[2] === artifactName) {
      return match[1].toLowerCase();
    }
  }
  throw new Error(`No checksum found for ${artifactName}`);
}

function sha256(filePath) {
  const hash = crypto.createHash("sha256");
  hash.update(fs.readFileSync(filePath));
  return hash.digest("hex");
}

function download(url, destination, redirectsRemaining = 5) {
  return new Promise((resolve, reject) => {
    const request = https.get(url, (response) => {
      const status = response.statusCode || 0;
      if (status >= 300 && status < 400 && response.headers.location) {
        response.resume();
        if (redirectsRemaining === 0) {
          reject(new Error(`Too many redirects while downloading ${url}`));
          return;
        }
        const next = new URL(response.headers.location, url).toString();
        download(next, destination, redirectsRemaining - 1).then(resolve, reject);
        return;
      }
      if (status !== 200) {
        response.resume();
        reject(new Error(`Download failed with HTTP ${status}: ${url}`));
        return;
      }

      const output = fs.createWriteStream(destination, { mode: 0o600 });
      response.pipe(output);
      output.on("finish", () => output.close(resolve));
      output.on("error", reject);
    });
    request.on("error", reject);
  });
}

function run(command, args) {
  const result = childProcess.spawnSync(command, args, {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  });
  if (result.error || result.status !== 0) {
    const detail = (result.stderr || result.error?.message || "").trim();
    throw new Error(
      `Could not extract the Melange CLI archive${detail ? `: ${detail}` : ""}`,
    );
  }
}

function extract(archivePath, target, destination) {
  if (target.extension === "zip") {
    const escapedArchive = archivePath.replace(/'/g, "''");
    const escapedDestination = destination.replace(/'/g, "''");
    run("powershell.exe", [
      "-NoLogo",
      "-NoProfile",
      "-NonInteractive",
      "-Command",
      `Expand-Archive -LiteralPath '${escapedArchive}' -DestinationPath '${escapedDestination}' -Force`,
    ]);
    return;
  }
  run("tar", ["-xzf", archivePath, "-C", destination, target.executable]);
}

async function install(options = {}) {
  const packageRoot = options.packageRoot || path.resolve(__dirname, "..");
  const packageVersion =
    options.packageVersion || require(path.join(packageRoot, "package.json")).version;
  const platform = options.platform || process.platform;
  const architecture = options.architecture || process.arch;
  const target = targetFor(platform, architecture);
  const artifact = archiveName(packageVersion, target);
  const checksums =
    options.checksums ||
    fs.readFileSync(path.join(packageRoot, "checksums.txt"), "utf8");
  const expected = checksumFor(checksums, artifact);
  const downloadArtifact = options.download || download;
  const extractArtifact = options.extract || extract;
  const temporaryRoot = fs.mkdtempSync(
    path.join(os.tmpdir(), "melange-cli-install-"),
  );
  const archivePath = path.join(temporaryRoot, artifact);
  const extractedPath = path.join(temporaryRoot, "extracted");
  const destination = path.join(
    packageRoot,
    "vendor",
    `${platform}-${architecture}`,
  );

  try {
    fs.mkdirSync(extractedPath, { recursive: true });
    const url = `${RELEASE_BASE_URL}/v${packageVersion}/${artifact}`;
    await downloadArtifact(url, archivePath);
    const actual = sha256(archivePath);
    if (!crypto.timingSafeEqual(Buffer.from(actual), Buffer.from(expected))) {
      throw new Error(`Checksum verification failed for ${artifact}`);
    }

    extractArtifact(archivePath, target, extractedPath);
    const executable = path.join(extractedPath, target.executable);
    if (!fs.statSync(executable).isFile()) {
      throw new Error(`Archive does not contain ${target.executable}`);
    }
    if (platform !== "win32") {
      fs.chmodSync(executable, 0o755);
    }

    fs.mkdirSync(path.dirname(destination), { recursive: true });
    fs.rmSync(destination, { recursive: true, force: true });
    fs.renameSync(extractedPath, destination);
  } finally {
    fs.rmSync(temporaryRoot, { recursive: true, force: true });
  }
}

module.exports = {
  archiveName,
  checksumFor,
  download,
  extract,
  install,
  targetFor,
};

if (require.main === module) {
  install().catch((error) => {
    process.stderr.write(`melange-cli install: ${error.message}\n`);
    process.exitCode = 1;
  });
}
