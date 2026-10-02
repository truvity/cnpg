#!/usr/bin/env bash
# A TLS-enabled PostgreSQL for the client conformance suites.
#
#   pg-tls.sh up    start it; print `export` lines on stdout (all else on stderr)
#   pg-tls.sh down  remove it (and the certificate directory)
#
# Everything is generated here, per run: a CA, a second unrelated CA, a server
# certificate that carries ONLY the name `localhost`, two client certificates
# for the certificate role, and a password role. Nothing is a secret and
# nothing outlives the run.
#
# The image is pinned by DIGEST. A GitHub `services:` container is not used
# because it starts before any step can generate the certificates it needs;
# this runs the same pinned image itself.
set -euo pipefail

IMAGE="postgres:17@sha256:67f41722b7a8cbdb868a44a4995c846eddfdc2973bccb291ce937dce88ad5675"
NAME="${CNPG_CLIENTS_PG_NAME:-cnpg-clients-pg-$$}"
STATE="${CNPG_CLIENTS_PG_STATE:-${TMPDIR:-/tmp}/cnpg-clients-pg.state}"

log() { echo "pg-tls: $*" >&2; }

up() {
  local dir
  dir="$(mktemp -d)"
  chmod 755 "$dir"
  cd "$dir"

  # --- authorities -------------------------------------------------------
  for ca in ca other-ca; do
    openssl ecparam -name prime256v1 -genkey -noout -out "$ca.key" 2>/dev/null
    openssl req -x509 -new -key "$ca.key" -sha256 -days 2 -subj "/CN=cnpg-clients-$ca" -out "$ca.crt" 2>/dev/null
  done

  # --- server: the name `localhost` and nothing else ----------------------
  openssl ecparam -name prime256v1 -genkey -noout -out server.key 2>/dev/null
  openssl req -new -key server.key -subj "/CN=localhost" -out server.csr 2>/dev/null
  printf 'subjectAltName=DNS:localhost\nextendedKeyUsage=serverAuth\n' >server.ext
  openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 2 -sha256 -extfile server.ext -out server.crt 2>/dev/null

  # --- clients: the role's CN, two certificates (rotation) ----------------
  for n in 1 2; do
    openssl ecparam -name prime256v1 -genkey -noout -out "app_cert.$n.key" 2>/dev/null
    openssl req -new -key "app_cert.$n.key" -subj "/CN=app_cert" -out "app_cert.$n.csr" 2>/dev/null
    printf 'extendedKeyUsage=clientAuth\n' >client.ext
    openssl x509 -req -in "app_cert.$n.csr" -CA ca.crt -CAkey ca.key -CAcreateserial -days 2 -sha256 -extfile client.ext -set_serial "0x${n}0${n}" -out "app_cert.$n.crt" 2>/dev/null
  done
  # a certificate with the right CN from the WRONG authority
  openssl ecparam -name prime256v1 -genkey -noout -out foreign.key 2>/dev/null
  openssl req -new -key foreign.key -subj "/CN=app_cert" -out foreign.csr 2>/dev/null
  openssl x509 -req -in foreign.csr -CA other-ca.crt -CAkey other-ca.key -CAcreateserial -days 2 -sha256 -extfile client.ext -out foreign.crt 2>/dev/null
  chmod 644 ./*
  chmod 600 ./*.key

  cat >pg_hba.conf <<'HBA'
local all all trust
hostssl all app_cert all cert
hostssl all app_pw all scram-sha-256
hostssl all postgres all scram-sha-256
HBA

  local pw
  pw="pw-$(openssl rand -hex 8)"

  # The server key must be owned by the postgres user and 0600; a bind mount
  # keeps the host owner, so the container installs its own copies first.
  docker run -d --name "$NAME" -p 127.0.0.1::5432 \
    -e POSTGRES_PASSWORD="$pw" -e POSTGRES_DB=app \
    -v "$dir:/host:ro" --entrypoint sh "$IMAGE" -c '
      set -e
      install -d -o postgres -g postgres -m 0700 /pgtls
      install -o postgres -g postgres -m 0600 /host/server.key /pgtls/server.key
      install -o postgres -g postgres -m 0644 /host/server.crt /host/ca.crt /host/pg_hba.conf /pgtls/
      exec docker-entrypoint.sh postgres \
        -c ssl=on -c ssl_cert_file=/pgtls/server.crt -c ssl_key_file=/pgtls/server.key \
        -c ssl_ca_file=/pgtls/ca.crt -c ssl_min_protocol_version=TLSv1.2 \
        -c hba_file=/pgtls/pg_hba.conf -c password_encryption=scram-sha-256 \
        -c listen_addresses=*
    ' >/dev/null

  echo "$NAME $dir" >"$STATE"

  local i
  for i in $(seq 1 60); do
    if docker exec "$NAME" psql -U postgres -d app -Atc 'select 1' >/dev/null 2>&1; then break; fi
    sleep 1
    if [ "$i" = 60 ]; then log "postgres did not become ready"; docker logs "$NAME" >&2 || true; exit 1; fi
  done
  # The entrypoint restarts the server once after its init; wait for it to settle.
  sleep 2
  for i in $(seq 1 30); do
    docker exec "$NAME" psql -U postgres -d app -Atc 'select 1' >/dev/null 2>&1 && break
    sleep 1
  done

  local pwrole="app-pw-$(openssl rand -hex 6)"
  docker exec -i "$NAME" psql -v ON_ERROR_STOP=1 -U postgres -d app >/dev/null <<SQL
create role app_cert login;
create role app_pw login password '$pwrole';
grant all on schema public to app_cert, app_pw;
SQL

  local port
  port="$(docker port "$NAME" 5432/tcp | head -1 | sed 's/.*://')"
  log "up: $NAME on 127.0.0.1:$port, certificates in $dir"

  cat <<ENV
export CNPG_CLIENTS_PG_HOST=localhost
export CNPG_CLIENTS_PG_ADDR=127.0.0.1
export CNPG_CLIENTS_PG_PORT=$port
export CNPG_CLIENTS_PG_DATABASE=app
export CNPG_CLIENTS_PG_DIR=$dir
export CNPG_CLIENTS_PG_PW_ROLE=app_pw
export CNPG_CLIENTS_PG_PW_PASSWORD=$pwrole
export CNPG_CLIENTS_PG_CONTAINER=$NAME
ENV
}

down() {
  [ -f "$STATE" ] || exit 0
  local name dir
  read -r name dir <"$STATE"
  docker rm -f "$name" >/dev/null 2>&1 || true
  [ -n "$dir" ] && rm -rf "$dir"
  rm -f "$STATE"
}

case "${1:-}" in
  up) up ;;
  down) down ;;
  *) echo "usage: $0 up|down" >&2; exit 2 ;;
esac
