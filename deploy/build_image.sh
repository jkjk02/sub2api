#!/usr/bin/env bash
# Build, verify, deploy, and roll back auditable Sub2API source images.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
BASE_COMPOSE="${SCRIPT_DIR}/docker-compose.local.yml"
BUILD_COMPOSE="${SCRIPT_DIR}/docker-compose.build.yml"
DOCKER_BIN="${DOCKER_BIN:-docker}"
IMAGE_REPOSITORY="${SUB2API_IMAGE_REPOSITORY:-sub2api-source}"

usage() {
    cat <<'EOF'
Usage: ./build_image.sh [command] [argument]

Commands:
  metadata              Print the Git-derived build metadata and image reference.
  config                Validate the merged Docker Compose configuration.
  build                 Build and verify the current repository source image (default).
  deploy                Build, verify, and start the commit-tagged image.
  verify                Verify the current commit-tagged image metadata and binary version.
  rollback TAG|IMAGE    Start a previously retained immutable image without rebuilding.
  status                Show the current Compose service status.
  help                  Show this help.

Examples:
  ./build_image.sh build
  ./build_image.sh deploy
  ./build_image.sh rollback git-0123456789ab
  SUB2API_IMAGE_REPOSITORY=registry.example/sub2api ./build_image.sh deploy

Production builds require a clean Git worktree. For local-only diagnostics, set
ALLOW_DIRTY=1; dirty images receive a unique dirty tag and are visibly labelled.
EOF
}

fail() {
    printf 'build_image.sh: %s\n' "$1" >&2
    exit 1
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

validate_image_repository() {
    case "$IMAGE_REPOSITORY" in
        ''|*[!A-Za-z0-9._/:_-]*) fail "invalid SUB2API_IMAGE_REPOSITORY: ${IMAGE_REPOSITORY}" ;;
    esac
}

load_build_metadata() {
    require_command git
    require_command date

    git -C "$REPO_ROOT" rev-parse --is-inside-work-tree >/dev/null 2>&1 || \
        fail "${REPO_ROOT} is not a Git worktree"

    local full_commit short_commit dirty_suffix resolved_version
    full_commit="$(git -C "$REPO_ROOT" rev-parse --verify HEAD)"
    short_commit="$(git -C "$REPO_ROOT" rev-parse --short=12 HEAD)"
    resolved_version="${SUB2API_BUILD_VERSION:-$("${REPO_ROOT}/backend/scripts/resolve-version.sh")}"

    [ -n "$resolved_version" ] || fail "resolved VERSION is empty"
    case "$resolved_version" in
        *[[:space:]]*) fail "VERSION must not contain whitespace: ${resolved_version}" ;;
    esac

    dirty_suffix=''
    SUB2API_BUILD_DIRTY=false
    if [ -n "$(git -C "$REPO_ROOT" status --porcelain --untracked-files=normal)" ]; then
        SUB2API_BUILD_DIRTY=true
        if [ "${ALLOW_DIRTY:-0}" != '1' ]; then
            fail 'Git worktree is dirty; commit the source first or set ALLOW_DIRTY=1 for a local diagnostic image'
        fi
        dirty_suffix="-dirty-$(date -u +%Y%m%d%H%M%S)"
    fi

    SUB2API_BUILD_VERSION="$resolved_version"
    SUB2API_BUILD_COMMIT="$full_commit"
    SUB2API_BUILD_DATE="${SUB2API_BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
    SUB2API_IMAGE_TAG="${SUB2API_IMAGE_TAG:-git-${short_commit}${dirty_suffix}}"

    case "$SUB2API_IMAGE_TAG" in
        ''|*[!A-Za-z0-9_.-]*) fail "invalid SUB2API_IMAGE_TAG: ${SUB2API_IMAGE_TAG}" ;;
    esac
    validate_image_repository

    SUB2API_IMAGE_REPOSITORY="$IMAGE_REPOSITORY"
    IMAGE_REF="${SUB2API_IMAGE_REPOSITORY}:${SUB2API_IMAGE_TAG}"

    export SUB2API_BUILD_VERSION SUB2API_BUILD_COMMIT SUB2API_BUILD_DATE
    export SUB2API_BUILD_DIRTY SUB2API_IMAGE_REPOSITORY SUB2API_IMAGE_TAG IMAGE_REF
}

require_docker_compose() {
    command -v "$DOCKER_BIN" >/dev/null 2>&1 || fail "Docker executable not found: ${DOCKER_BIN}"
    "$DOCKER_BIN" compose version >/dev/null 2>&1 || fail 'Docker Compose v2 is required'
}

compose() {
    "$DOCKER_BIN" compose \
        -f "$BASE_COMPOSE" \
        -f "$BUILD_COMPOSE" \
        "$@"
}

validate_compose() {
    require_docker_compose
    compose config --quiet
}

print_metadata() {
    printf 'VERSION=%s\n' "$SUB2API_BUILD_VERSION"
    printf 'COMMIT=%s\n' "$SUB2API_BUILD_COMMIT"
    printf 'DATE=%s\n' "$SUB2API_BUILD_DATE"
    printf 'DIRTY=%s\n' "$SUB2API_BUILD_DIRTY"
    printf 'IMAGE=%s\n' "$IMAGE_REF"
}

verify_current_image() {
    require_docker_compose

    local actual_version actual_commit actual_date version_output expected_output
    "$DOCKER_BIN" image inspect "$IMAGE_REF" >/dev/null 2>&1 || \
        fail "image does not exist locally: ${IMAGE_REF}"

    actual_version="$("$DOCKER_BIN" image inspect --format '{{ index .Config.Labels "org.opencontainers.image.version" }}' "$IMAGE_REF")"
    actual_commit="$("$DOCKER_BIN" image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$IMAGE_REF")"
    actual_date="$("$DOCKER_BIN" image inspect --format '{{ index .Config.Labels "org.opencontainers.image.created" }}' "$IMAGE_REF")"

    [ "$actual_version" = "$SUB2API_BUILD_VERSION" ] || \
        fail "image VERSION label mismatch: expected ${SUB2API_BUILD_VERSION}, got ${actual_version}"
    [ "$actual_commit" = "$SUB2API_BUILD_COMMIT" ] || \
        fail "image COMMIT label mismatch: expected ${SUB2API_BUILD_COMMIT}, got ${actual_commit}"
    [ "$actual_date" = "$SUB2API_BUILD_DATE" ] || \
        fail "image DATE label mismatch: expected ${SUB2API_BUILD_DATE}, got ${actual_date}"

    version_output="$("$DOCKER_BIN" run --rm --entrypoint /app/sub2api "$IMAGE_REF" -version 2>&1)"
    expected_output="Sub2API ${SUB2API_BUILD_VERSION} (commit: ${SUB2API_BUILD_COMMIT}, built: ${SUB2API_BUILD_DATE})"
    case "$version_output" in
        *"$expected_output"*) ;;
        *) fail "binary provenance mismatch; expected output containing: ${expected_output}" ;;
    esac

    printf 'verified image: %s\n' "$IMAGE_REF"
    printf '%s\n' "$expected_output"
}

build_current_image() {
    validate_compose

    if [ "${PULL_BASE_IMAGES:-0}" = '1' ]; then
        compose build --pull sub2api
    else
        compose build sub2api
    fi

    verify_current_image
}

deploy_current_image() {
    local previous_image
    previous_image="$("$DOCKER_BIN" inspect --format '{{.Config.Image}}' sub2api 2>/dev/null || true)"

    build_current_image
    compose up -d --no-build sub2api

    printf 'deployed source image: %s\n' "$IMAGE_REF"
    if [ -n "$previous_image" ] && [ "$previous_image" != "$IMAGE_REF" ]; then
        printf 'rollback command: %s rollback %s\n' "$0" "$previous_image"
    else
        printf 'rollback command: %s rollback TAG_OR_IMAGE\n' "$0"
    fi
}

rollback_image() {
    local target="$1" rollback_repository rollback_tag rollback_ref

    case "$target" in
        *:*)
            rollback_repository="${target%:*}"
            rollback_tag="${target##*:}"
            ;;
        *)
            rollback_repository="$IMAGE_REPOSITORY"
            rollback_tag="$target"
            ;;
    esac

    [ -n "$rollback_repository" ] || fail 'rollback image repository is empty'
    [ -n "$rollback_tag" ] || fail 'rollback image tag is empty'

    SUB2API_IMAGE_REPOSITORY="$rollback_repository"
    SUB2API_IMAGE_TAG="$rollback_tag"
    IMAGE_REF="${SUB2API_IMAGE_REPOSITORY}:${SUB2API_IMAGE_TAG}"
    export SUB2API_IMAGE_REPOSITORY SUB2API_IMAGE_TAG IMAGE_REF

    require_docker_compose
    "$DOCKER_BIN" image inspect "$IMAGE_REF" >/dev/null 2>&1 || \
        fail "rollback image does not exist locally: ${IMAGE_REF}"

    # Rollback must remain available even when the checkout is dirty or absent from
    # the image's original commit. Populate Compose provenance from the retained
    # image itself instead of the current worktree so runtime labels stay truthful.
    SUB2API_BUILD_VERSION="$("$DOCKER_BIN" image inspect --format '{{ index .Config.Labels "org.opencontainers.image.version" }}' "$IMAGE_REF")"
    SUB2API_BUILD_COMMIT="$("$DOCKER_BIN" image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$IMAGE_REF")"
    SUB2API_BUILD_DATE="$("$DOCKER_BIN" image inspect --format '{{ index .Config.Labels "org.opencontainers.image.created" }}' "$IMAGE_REF")"
    SUB2API_BUILD_DIRTY="$("$DOCKER_BIN" image inspect --format '{{ index .Config.Labels "io.sub2api.source.dirty" }}' "$IMAGE_REF")"
    SUB2API_BUILD_VERSION="${SUB2API_BUILD_VERSION:-rollback}"
    SUB2API_BUILD_COMMIT="${SUB2API_BUILD_COMMIT:-unknown}"
    SUB2API_BUILD_DATE="${SUB2API_BUILD_DATE:-1970-01-01T00:00:00Z}"
    SUB2API_BUILD_DIRTY="${SUB2API_BUILD_DIRTY:-false}"
    export SUB2API_BUILD_VERSION SUB2API_BUILD_COMMIT SUB2API_BUILD_DATE SUB2API_BUILD_DIRTY

    compose config --quiet
    compose up -d --no-build sub2api

    printf 'rolled back to retained image: %s\n' "$IMAGE_REF"
    printf 'runtime provenance:\n'
    "$DOCKER_BIN" run --rm --entrypoint /app/sub2api "$IMAGE_REF" -version 2>&1
}

main() {
    local command_name="${1:-build}"

    case "$command_name" in
        help|-h|--help)
            usage
            return
            ;;
    esac

    if [ "$command_name" = 'rollback' ]; then
        [ "$#" -eq 2 ] || fail 'rollback requires TAG or IMAGE argument'
        rollback_image "$2"
        return
    fi

    load_build_metadata

    case "$command_name" in
        metadata)
            print_metadata
            ;;
        config)
            validate_compose
            print_metadata
            printf 'merged Compose configuration is valid\n'
            ;;
        build)
            print_metadata
            build_current_image
            ;;
        deploy|run)
            print_metadata
            deploy_current_image
            ;;
        verify)
            print_metadata
            verify_current_image
            ;;
        status)
            validate_compose
            compose ps
            ;;
        *)
            usage >&2
            fail "unknown command: ${command_name}"
            ;;
    esac
}

main "$@"
