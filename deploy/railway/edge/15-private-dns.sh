#!/bin/sh
set -eu
# Резолвер берётся из контейнера Railway, без адреса Docker 127.0.0.11.
DNS_RESOLVER=$(awk '/^nameserver / {print $2; exit}' /etc/resolv.conf)
test -n "$DNS_RESOLVER"
case "$DNS_RESOLVER" in *:*) DNS_RESOLVER="[$DNS_RESOLVER]" ;; esac
export DNS_RESOLVER
