# A stand-in for the VPS: Ubuntu 24.04 with systemd as PID 1, so that the real deploy/install.sh
# runs inside exactly as it does on a server (systemd units, nginx, ufw, fail2ban).
# Started by deploy/docker/staging/staging.sh — it needs --privileged and the cgroup mount.
FROM ubuntu:24.04

ENV container=docker \
    DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8

# What a fresh Ubuntu Server image has and a container image lacks.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        systemd systemd-sysv dbus ca-certificates curl git sudo iproute2 iptables kmod procps \
        openssh-server rsyslog less python3 \
    && rm -rf /var/lib/apt/lists/* \
    # Units that make no sense without real hardware or a console.
    && systemctl mask \
        getty.target console-getty.service systemd-logind.service systemd-udevd.service \
        systemd-udev-trigger.service systemd-modules-load.service sys-kernel-config.mount \
        sys-kernel-debug.mount sys-kernel-tracing.mount e2scrub_reap.service \
    && systemctl enable ssh

STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
