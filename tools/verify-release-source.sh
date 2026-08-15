#!/usr/bin/env bash
set -euo pipefail

: "${GH_TOKEN:?GH_TOKEN is required}"
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
: "${RELEASE_SHA:?RELEASE_SHA is required}"
: "${RELEASE_TAG:?RELEASE_TAG is required}"

if [[ ! "$RELEASE_SHA" =~ ^[0-9a-f]{40}$ ]]; then
  echo "RELEASE_SHA must be a full lowercase commit SHA" >&2
  exit 1
fi
if [[ ! "$RELEASE_TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "RELEASE_TAG must be a stable vMAJOR.MINOR.PATCH tag" >&2
  exit 1
fi

main_sha="$(gh api "repos/${GITHUB_REPOSITORY}/git/ref/heads/main" --jq '.object.sha')"
if [[ "$main_sha" != "$RELEASE_SHA" ]]; then
  echo "current origin/main is ${main_sha}, not approved SHA ${RELEASE_SHA}" >&2
  exit 1
fi

tag_ref="$(gh api "repos/${GITHUB_REPOSITORY}/git/ref/tags/${RELEASE_TAG}")"
tag_object_type="$(jq -r '.object.type' <<<"$tag_ref")"
tag_object_sha="$(jq -r '.object.sha' <<<"$tag_ref")"
if [[ "$tag_object_type" != "tag" || ! "$tag_object_sha" =~ ^[0-9a-f]{40}$ ]]; then
  echo "${RELEASE_TAG} must be an annotated tag, not a lightweight tag" >&2
  exit 1
fi
if [[ -n "${EXPECTED_TAG_OBJECT_SHA:-}" && "$tag_object_sha" != "$EXPECTED_TAG_OBJECT_SHA" ]]; then
  echo "remote tag moved after approval: ${tag_object_sha} != ${EXPECTED_TAG_OBJECT_SHA}" >&2
  exit 1
fi

tag_object="$(gh api "repos/${GITHUB_REPOSITORY}/git/tags/${tag_object_sha}")"
peeled_type="$(jq -r '.object.type' <<<"$tag_object")"
peeled_sha="$(jq -r '.object.sha' <<<"$tag_object")"
verified="$(jq -r '.verification.verified' <<<"$tag_object")"
verification_reason="$(jq -r '.verification.reason' <<<"$tag_object")"
tag_name="$(jq -r '.tag' <<<"$tag_object")"
if [[ "$tag_name" != "$RELEASE_TAG" || "$peeled_type" != "commit" || "$peeled_sha" != "$RELEASE_SHA" ]]; then
  echo "annotated tag does not peel to the approved commit" >&2
  exit 1
fi
if [[ "$verified" != "true" || "$verification_reason" != "valid" ]]; then
  echo "GitHub did not verify the tag signature: ${verification_reason}" >&2
  exit 1
fi

if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  echo "tag_object_sha=$tag_object_sha" >> "$GITHUB_OUTPUT"
fi
printf 'verified %s: tag object %s -> commit %s on current main\n' \
  "$RELEASE_TAG" "$tag_object_sha" "$RELEASE_SHA"
