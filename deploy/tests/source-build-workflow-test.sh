#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo_root"

fail() {
    printf 'source build workflow test failed: %s\n' "$1" >&2
    exit 1
}

assert_contains() {
    file=$1
    value=$2
    grep -Fq -- "$value" "$file" || fail "$file is missing: $value"
}

bash -n deploy/build_image.sh

assert_contains deploy/docker-compose.build.yml 'context: ..'
assert_contains deploy/docker-compose.build.yml 'dockerfile: Dockerfile'
assert_contains deploy/docker-compose.build.yml 'VERSION: "${SUB2API_BUILD_VERSION:?'
assert_contains deploy/docker-compose.build.yml 'COMMIT: "${SUB2API_BUILD_COMMIT:?'
assert_contains deploy/docker-compose.build.yml 'DATE: "${SUB2API_BUILD_DATE:?'
assert_contains deploy/docker-compose.build.yml 'org.opencontainers.image.revision: "${SUB2API_BUILD_COMMIT}"'
assert_contains deploy/docker-compose.build.yml 'image: "${SUB2API_IMAGE_REPOSITORY:-sub2api-source}:${SUB2API_IMAGE_TAG:?'
assert_contains deploy/docker-compose.build.yml 'pull_policy: never'

work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT HUP INT TERM
fake_docker="$work_dir/docker"
docker_log="$work_dir/docker.log"
output_log="$work_dir/output.log"

cat >"$fake_docker" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$DOCKER_LOG"

if [ "${1:-}" = 'inspect' ]; then
    printf '%s\n' 'sub2api-source:git-previous'
    exit 0
fi

if [ "${1:-}" = 'image' ] && [ "${2:-}" = 'inspect' ]; then
    case "$*" in
        *org.opencontainers.image.version*) printf '%s\n' "${SUB2API_BUILD_VERSION:-rollback-version}" ;;
        *org.opencontainers.image.revision*) printf '%s\n' "${SUB2API_BUILD_COMMIT:-rollback-commit}" ;;
        *org.opencontainers.image.created*) printf '%s\n' "${SUB2API_BUILD_DATE:-2026-08-20T00:00:00Z}" ;;
        *io.sub2api.source.dirty*) printf '%s\n' "${SUB2API_BUILD_DIRTY:-false}" ;;
    esac
    exit 0
fi

if [ "${1:-}" = 'run' ]; then
    printf 'Sub2API %s (commit: %s, built: %s)\n' \
        "${SUB2API_BUILD_VERSION:-rollback-version}" "${SUB2API_BUILD_COMMIT:-rollback-commit}" "${SUB2API_BUILD_DATE:-2026-08-20T00:00:00Z}"
    exit 0
fi

exit 0
EOF
chmod +x "$fake_docker"

DOCKER_BIN="$fake_docker" \
DOCKER_LOG="$docker_log" \
ALLOW_DIRTY=1 \
SUB2API_BUILD_DATE=2026-08-21T00:00:00Z \
bash deploy/build_image.sh deploy >"$output_log"

assert_contains "$docker_log" 'compose version'
assert_contains "$docker_log" 'docker-compose.local.yml -f'
assert_contains "$docker_log" 'docker-compose.build.yml config --quiet'
assert_contains "$docker_log" 'docker-compose.build.yml build sub2api'
assert_contains "$docker_log" 'docker-compose.build.yml up -d --no-build sub2api'
assert_contains "$output_log" 'verified image: sub2api-source:git-'
assert_contains "$output_log" 'rollback command:'

DOCKER_BIN="$fake_docker" \
DOCKER_LOG="$docker_log" \
ALLOW_DIRTY=1 \
SUB2API_BUILD_DATE=2026-08-21T00:00:00Z \
bash deploy/build_image.sh rollback git-previous >>"$output_log"

assert_contains "$output_log" 'rolled back to retained image: sub2api-source:git-previous'

printf 'source build workflow test passed\n'
