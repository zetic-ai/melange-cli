---
name: melange-liquid-event
description: Temporary event skill for building ZETIC Melange × Liquid AI apps with Melange SDK 1.11.0 and approved Liquid AI models. It expires at 2026-10-07T00:00:00-07:00 (San Francisco time).
---

# Melange × Liquid AI event apps

## Validity

This skill is valid through `2026-10-06 23:59:59` in
`America/Los_Angeles` (San Francisco), and expires at
`2026-10-07T00:00:00-07:00`. Before using it for a new event task, check the
current time in that time zone. At or after the expiry instant, do not follow
this skill for new work; state that it has expired and request an updated
event skill.

For an AI feature request in this event, use the Melange SDK first. The Liquid
AI LFM allowlist below applies only to the LLM that generates final answers.
Do not replace that LLM with another model merely because it appears easier to
integrate.

## Event skill, account, and CLI setup

1. The standard repository installer installs this event skill alongside
   `melange-cli`. If installing skills manually, use this command, then restart
   the coding agent:

   ```sh
   npx skills add zetic-ai/melange-cli --skill melange-liquid-event --global
   ```

2. Confirm that the participant has an account, or direct them to
   <https://melange.zetic.ai/>.
   When registering a new account, apply the invitation code `HOUSTON2026`.
3. Install or update to the latest `melange` CLI. On macOS or Linux, use the
   repository's installer with `--cli-only`:

   ```sh
   curl -fsSL https://raw.githubusercontent.com/zetic-ai/melange-cli/main/script/install.sh \
     | sh -s -- --cli-only
   ```

   On Windows, use `npm install -g @zetic-ai/melange-cli`. The standard
   installer installs both `melange-cli` and this event skill; `--cli-only`
   skips both skills.
4. Check authentication with `melange auth status --json`. If it is not
   authenticated, run `melange auth login`, then check the status again.
5. Direct the participant to [Melange Settings → Personal Access
   Tokens](https://melange.zetic.ai/settings?tab=pat) to issue the Personal
   Access Token used by the SDK.

Never use `melange auth token` to obtain an SDK key or inspect credentials. It
prints the active resolved credential, which may be OAuth or a PAT, to stdout.
Do not expose a token value in chat, logs, source code, or Git commits.

## Approved LLMs for answer generation

Use only these five Liquid AI LFM public-library addresses for LLM answer
generation. Each listed value is the Melange SDK's `name` argument; it is not
the opaque `MODEL_KEY` returned by `melange model` commands.

| Model | SDK `name` | Event-approved capabilities |
| --- | --- | --- |
| Liquid AI LFM2.5-230M | `zetic/LFM2.5-230M` | KV Persistence, RAG, Function Calling |
| Liquid AI LFM2.5-350M | `zetic/LFM2.5-350M` | KV Persistence, RAG, Function Calling |
| Liquid AI LFM2.5-1.2B-Instruct | `zetic/LFM2.5-1.2B-Instruct` | KV Persistence, RAG, Function Calling |
| Liquid AI LFM2.5-VL-450M | `zetic/LFM2.5-VL-450M` | Image Responding |
| Liquid AI LFM2.5-2.6B | `zetic/LFM2.5-2.6B` | KV Persistence, RAG, Function Calling |

This LLM allowlist does not itself restrict non-LLM app components. The RAG
embedder, when RAG is implemented, has its own fixed requirement below. Do not
re-upload or reconvert any listed LLM. If an approved LLM cannot implement a
request, explain the limitation and propose an adjusted feature.

Within this event's allowlist, call `respond(..., image)` only for a model
initialized with `name = "zetic/LFM2.5-VL-450M"`. Do not claim unlisted RAG,
KV-persistence, or function-calling support for that VL model.

## Melange SDK requirements

The Melange SDK works only on physical devices. It does not run on Android
emulators or the iOS Simulator; use a supported physical device for all SDK
verification.

Use Melange SDK version `1.11.0`. If that exact version cannot be installed or
resolved, report the cause and do not substitute another version. Use the
[Melange API Reference](https://docs.zetic.ai/) together with the installed
1.11.0 API; if they differ, report the mismatch instead of copying an older
API or changing versions.

### Android installation

Android SDK `1.11.0` is distributed from the GitHub Release, not Maven
Central. Follow the [public distribution
instructions](https://github.com/zetic-ai/ZeticMLangeAndroid/tree/feature/20261006-github-public-distribution)
and resolve the SDK from the [1.11.0
release](https://github.com/zetic-ai/ZeticMLangeAndroid/releases/tag/1.11.0).
In the consuming application's `settings.gradle.kts`, keep `google()` and
`mavenCentral()` for public dependencies and add this Ivy repository:

```kotlin
dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.PREFER_SETTINGS)
    repositories {
        google()
        mavenCentral()
        ivy {
            name = "ZeticMLangeAndroidGitHubRelease"
            url = uri("https://github.com/zetic-ai/ZeticMLangeAndroid/releases/download")
            patternLayout {
                artifact("[revision]/[artifact]-[revision](-[classifier]).[ext]")
            }
            metadataSources {
                gradleMetadata()
            }
            content {
                includeGroup("com.zeticai.mlange")
                includeGroup("com.zeticai.mlange.backend")
            }
        }
    }
}
```

Then add the exact SDK dependency in the app module:

```kotlin
dependencies {
    implementation("com.zeticai.mlange:mlange:1.11.0")
}
```

The release tag and dependency version must match. Do not use Maven Central as
the source of `com.zeticai.mlange:mlange:1.11.0`; it remains in the repository
list only for dependencies outside the GitHub Release. If the GitHub Release
repository cannot resolve that exact version, report the cause instead of
substituting another repository or SDK version.

### iOS installation

Add [ZeticMLangeiOS](https://github.com/zetic-ai/ZeticMLangeiOS) through Swift
Package Manager with the repository URL
`https://github.com/zetic-ai/ZeticMLangeiOS.git`, exact version `1.11.0`, and
product `ZeticMLange`. In a `Package.swift` manifest, use:

```swift
.package(url: "https://github.com/zetic-ai/ZeticMLangeiOS.git", exact: "1.11.0")
```

Use `ZeticMLange` from the `ZeticMLangeiOS` package as the target dependency.
Do not use the repository's `main` branch or substitute another SDK version. If
the exact version cannot resolve, report the cause rather than falling back.

On Android, add this setting in the application module's Gradle configuration:

```kotlin
android {
    packaging {
        jniLibs {
            useLegacyPackaging = true
        }
    }
}
```

## RAG requirements

For RAG, download only this external GGUF embedder:

<https://huggingface.co/LiquidAI/LFM2.5-Embedding-350M-GGUF/resolve/a80de9c5b941d429104f0038292a0ef5a860e486/LFM2.5-Embedding-350M-Q4_K_M.gguf>

Do not require or download an external decoder GGUF. The final answer must be
generated by an approved model loaded through the Melange SDK. For RAG using
one of the event's RAG-capable text models, use `RagProfile.lfm25()` so the
retrieval prompt format matches the generation model. If the resolved SDK
cannot create local RAG without a decoder path, report that blocker; do not
work around it with a decoder GGUF or another SDK version.

Use CLS pooling for this embedder:

| Platform | Pooling setting |
| --- | --- |
| Android | `RagEmbedderPooling.CLS` |
| iOS | `.cls` |

Apply `query: ` to the question and `document: ` to each document chunk only
when constructing embedding inputs. Keep the original user question for the
generation request; do not add an embedding prefix to the final LLM prompt.
Do not assume the SDK adds either prefix automatically: confirm that behavior
in the resolved SDK, or explicitly apply the prefixes in the embedding path.

Keep every embedding input, including its prefix and tokenizer-added tokens,
within the embedder's 512-token limit. Character-count chunking alone does not
prove this limit. If implementing vector ranking outside the SDK's local RAG
pipeline, normalize embeddings and use cosine similarity (or normalized dot
product).

## Completion evidence

Use SDK 1.11.0 and an approved model on a physical device to verify model
initialization and a first response. If that execution is not verified, do not
report completion; instead state the verified steps and the remaining blocker.

For an implemented RAG feature, completion requires both of the following:

1. A top retrieved chunk contains the factual evidence needed to answer the
   test question.
2. The generated answer agrees with that evidence.

Use a test question whose answer is present in the indexed documents. If either
condition is not verified, do not report the RAG implementation as complete.
