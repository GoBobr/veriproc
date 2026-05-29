#!/bin/sh
# entrypoint.sh — prepare the runtime environment for an arbitrary-UID container.
#
# 1. Synthesise a /etc/passwd entry for the running UID so that tools such as
#    SSH (which call getpwuid()) can resolve the current user.
# 2. Copy $HOME/.ssh to a private writable directory and enforce strict
#    permissions (700 on the directory, 600 on private keys). SSH refuses to
#    use key files that are readable by group or others, which commonly occurs
#    when the source .ssh directory is bind-mounted read-only from a host
#    where a different user or process owns or shares the files.
set -e

CUR_UID=$(id -u)
CUR_GID=$(id -g)
USER_HOME="${HOME:-/tmp}"

# ── 1. /etc/passwd entry ───────────────────────────────────────────────────
if ! grep -q ":${CUR_UID}:" /etc/passwd 2>/dev/null; then
    echo "veriproc:x:${CUR_UID}:${CUR_GID}:VeriProc Daemon:${USER_HOME}:/sbin/nologin" >> /etc/passwd
fi

# ── 2. SSH key permissions ─────────────────────────────────────────────────
# Work only when a mounted .ssh directory exists but may have wrong permissions.
SRC_SSH="${USER_HOME}/.ssh"
TMP_SSH="/tmp/.ssh-veriproc"

if [ -d "${SRC_SSH}" ] && [ ! -d "${TMP_SSH}" ]; then
    mkdir -p "${TMP_SSH}"
    chmod 700 "${TMP_SSH}"
    # Copy all files; ignore errors for items we cannot read.
    cp -p "${SRC_SSH}"/. "${TMP_SSH}"/ 2>/dev/null || true
    # Private keys: owner read-write only.
    chmod 600 "${TMP_SSH}"/id_* 2>/dev/null || true
    # known_hosts and config: owner read-write only.
    chmod 600 "${TMP_SSH}"/known_hosts "${TMP_SSH}"/config 2>/dev/null || true
    # Point HOME to the parent so SSH finds /tmp/.ssh-veriproc as ~/.ssh.
    # We create a symlink under a private HOME so the path is predictable.
    TMP_HOME="/tmp/home-veriproc"
    mkdir -p "${TMP_HOME}"
    ln -sfn "${TMP_SSH}" "${TMP_HOME}/.ssh"
    export HOME="${TMP_HOME}"
fi

exec "$@"
