{{/*
cnpg-client: connect to a CloudNativePG cluster with a client certificate
and sslmode=verify-full.

Every helper takes ONE dict:

  (dict "context" $ "client" .Values.postgres)

`context` is the calling chart's root context (for .Release.Namespace);
`client` is a map of the inputs below. Naming contract, shared with
cnpg-cluster:

  client certificate  Secret  <cluster>-role-<role>   ("_" in the role becomes "-"); tls.crt, tls.key, key.der (optional)
  server CA           Secret  <cluster>-server-ca      ca.crt

Inputs (client):
  cluster          required  the CNPG Cluster name
  role             required  the PostgreSQL role the certificate names (PGUSER, unslugged)
  database                   PGDATABASE; omitted when empty
  namespace                  the cluster's namespace; default the release's
  service                    rw (default) | ro | r
  clusterDomain              cluster DNS domain; empty (default) renders the
                             short <svc>.<ns>.svc form
  port                       default 5432
  sslmode                    only verify-full is accepted; there to be refused
  mountPath                  default /var/run/cnpg-client
  volumeName                 default cnpg-client
  keyDer                     true: also project the DER PKCS#8 key (key.der),
                             needed by pgjdbc
  certMode, keyMode          file modes, default 0444 / 0440 (see below)
  dsnEnv                     name of an env var to carry a postgresql:// URL
  jdbcEnv                    name of an env var to carry a jdbc:postgresql:// URL
                             (requires keyDer)
  sslfactory                 JDBC only; default org.postgresql.ssl.jdbc4.LibPQFactory
  podSelector                networkPolicy only, required there: matchLabels
                             of the application's pods
  clusterNamespaceSelector   networkPolicy only: a true value adds a
                             namespaceSelector for the cluster's namespace
                             (for a cluster outside the release's namespace)
  name                       networkPolicy only: object name

The key file is owned by root inside the projected volume. libpq refuses a
private key readable by anyone but its owner unless the owner is root and
the mode is at most 0640, so the default 0440 needs the pod to set
securityContext.fsGroup (the group then reads it). Every define here is
prefixed with the chart name: a library chart's templates compile into the
CONSUMER's namespace, where a short name would collide.
*/}}

{{/*
Refuse a key nobody reads. A library template takes a dict, so it has no
values.schema.json to catch a misspelling for it.
*/}}
{{- define "cnpg-client.config" -}}
{{- $c := .client | default dict -}}
{{- $known := list "cluster" "role" "database" "namespace" "service" "clusterDomain" "port" "sslmode" "mountPath" "volumeName" "keyDer" "certMode" "keyMode" "dsnEnv" "jdbcEnv" "sslfactory" "podSelector" "clusterNamespaceSelector" "name" -}}
{{- range $k, $_ := $c -}}
{{- if not (has $k $known) -}}
{{- fail (printf "cnpg-client: %q is not an input — known: %s" $k (join ", " $known)) -}}
{{- end -}}
{{- end -}}
{{- $cluster := $c.cluster | default "" | toString -}}
{{- $role := $c.role | default "" | toString -}}
{{- if not $cluster -}}{{- fail "cnpg-client: cluster is required" -}}{{- end -}}
{{- if not $role -}}{{- fail "cnpg-client: role is required" -}}{{- end -}}
{{- if hasPrefix "pg_" $role -}}{{- fail (printf "cnpg-client: role %q is a built-in pg_ role; a client connects as an application role" $role) -}}{{- end -}}
{{- /* Both land in Secret names and DNS names, so they are DNS labels. */ -}}
{{- if not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$" $cluster) -}}{{- fail (printf "cnpg-client: cluster %q is not a DNS-1123 label" $cluster) -}}{{- end -}}
{{- /* The role keeps its real name (PGUSER, the certificate CN); the Secret name slugs "_" to "-". */ -}}
{{- $slug := replace "_" "-" $role -}}
{{- if not (regexMatch "^[a-z0-9_]([-a-z0-9_]*[a-z0-9_])?$" $role) -}}{{- fail (printf "cnpg-client: role %q must be lower-case letters, digits, - and _" $role) -}}{{- end -}}
{{- if not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$" $slug) -}}{{- fail (printf "cnpg-client: role %q cannot name a Secret (%s-role-%s is not a DNS-1123 label)" $role $cluster $slug) -}}{{- end -}}
{{- $sslmode := $c.sslmode | default "verify-full" -}}
{{- if ne $sslmode "verify-full" -}}{{- fail (printf "cnpg-client: sslmode %q is refused; only verify-full is accepted" $sslmode) -}}{{- end -}}
{{- $service := $c.service | default "rw" -}}
{{- if not (has $service (list "rw" "ro" "r")) -}}{{- fail (printf "cnpg-client: service %q is not rw, ro or r" $service) -}}{{- end -}}
{{- $ns := $c.namespace | default .context.Release.Namespace -}}
{{- $domain := $c.clusterDomain | default "" -}}
{{- if and $domain (not (regexMatch "^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$" $domain)) -}}{{- fail (printf "cnpg-client: clusterDomain %q is not a DNS name" $domain) -}}{{- end -}}
{{- $host := printf "%s-%s.%s.svc" $cluster $service $ns -}}
{{- if $domain -}}{{- $host = printf "%s.%s" $host $domain -}}{{- end -}}
{{- $port := int ($c.port | default 5432) -}}
{{- if or (lt $port 1) (gt $port 65535) -}}{{- fail (printf "cnpg-client: port %d is out of range" $port) -}}{{- end -}}
{{- $mount := $c.mountPath | default "/var/run/cnpg-client" -}}
{{- if not (hasPrefix "/" $mount) -}}{{- fail "cnpg-client: mountPath must be absolute" -}}{{- end -}}
{{- $mount = trimSuffix "/" $mount -}}
{{- $keyDer := $c.keyDer | default false -}}
{{- if and $c.jdbcEnv (not $keyDer) -}}{{- fail "cnpg-client: jdbcEnv needs keyDer: true (pgjdbc reads the key as DER PKCS#8)" -}}{{- end -}}
{{- dict "cluster" $cluster "role" $role "roleSlug" $slug "database" ($c.database | default "") "namespace" $ns "service" $service "host" $host "port" $port "mount" $mount "volumeName" ($c.volumeName | default "cnpg-client") "keyDer" $keyDer "certMode" ($c.certMode | default "0444") "keyMode" ($c.keyMode | default "0440") "dsnEnv" ($c.dsnEnv | default "") "jdbcEnv" ($c.jdbcEnv | default "") "sslfactory" ($c.sslfactory | default "org.postgresql.ssl.jdbc4.LibPQFactory") "podSelector" (get $c "podSelector") "clusterNamespaceSelector" ($c.clusterNamespaceSelector | default false) "name" ($c.name | default "") | toYaml -}}
{{- end -}}

{{/*
volumes: one projected volume holding the role's client certificate and the
server CA. Render under the pod's `volumes:`.
*/}}
{{- define "cnpg-client.volumes" -}}
{{- $p := include "cnpg-client.config" . | fromYaml -}}
- name: {{ $p.volumeName }}
  projected:
    sources:
      - secret:
          name: {{ printf "%s-role-%s" $p.cluster $p.roleSlug }}
          items:
            - key: tls.crt
              path: tls.crt
              mode: {{ $p.certMode }}
            - key: tls.key
              path: tls.key
              mode: {{ $p.keyMode }}
{{- if $p.keyDer }}
            - key: key.der
              path: key.der
              mode: {{ $p.keyMode }}
{{- end }}
      - secret:
          name: {{ printf "%s-server-ca" $p.cluster }}
          items:
            - key: ca.crt
              path: ca.crt
              mode: {{ $p.certMode }}
{{- end -}}

{{/*
volumeMounts: the projected directory, read-only. Render under the
container's `volumeMounts:`.
*/}}
{{- define "cnpg-client.volumeMounts" -}}
{{- $p := include "cnpg-client.config" . | fromYaml -}}
- name: {{ $p.volumeName }}
  mountPath: {{ $p.mount | quote }}
  readOnly: true
{{- end -}}

{{/*
env: the libpq environment, verify-full, plus the optional URL variables.
Render under the container's `env:`.
*/}}
{{- define "cnpg-client.env" -}}
{{- $p := include "cnpg-client.config" . | fromYaml -}}
{{- $crt := printf "%s/tls.crt" $p.mount -}}
{{- $key := printf "%s/tls.key" $p.mount -}}
{{- $ca := printf "%s/ca.crt" $p.mount -}}
- name: PGHOST
  value: {{ $p.host | quote }}
- name: PGPORT
  value: {{ $p.port | toString | quote }}
{{- if $p.database }}
- name: PGDATABASE
  value: {{ $p.database | quote }}
{{- end }}
- name: PGUSER
  value: {{ $p.role | quote }}
- name: PGSSLMODE
  value: verify-full
- name: PGSSLROOTCERT
  value: {{ $ca | quote }}
- name: PGSSLCERT
  value: {{ $crt | quote }}
- name: PGSSLKEY
  value: {{ $key | quote }}
{{- $path := ternary (printf "/%s" $p.database) "" (ne $p.database "") }}
{{- if $p.dsnEnv }}
- name: {{ $p.dsnEnv }}
  value: {{ printf "postgresql://%s@%s:%d%s?sslmode=verify-full&sslrootcert=%s&sslcert=%s&sslkey=%s" $p.role $p.host (int $p.port) $path $ca $crt $key | quote }}
{{- end }}
{{- if $p.jdbcEnv }}
- name: {{ $p.jdbcEnv }}
  value: {{ printf "jdbc:postgresql://%s:%d%s?user=%s&sslmode=verify-full&sslfactory=%s&sslrootcert=%s&sslcert=%s&sslkey=%s" $p.host (int $p.port) $path $p.role $p.sslfactory $ca $crt (printf "%s/key.der" $p.mount) | quote }}
{{- end }}
{{- end -}}

{{/*
networkPolicy: egress from the application's pods (client.podSelector) to
the cluster's pods on the PostgreSQL port. A NetworkPolicy that selects pods
for Egress denies the rest of their egress, DNS included; policies add up,
so the application's own chart grants what else it needs.
*/}}
{{- define "cnpg-client.networkPolicy" -}}
{{- $p := include "cnpg-client.config" . | fromYaml -}}
{{- if not (kindIs "map" $p.podSelector) -}}{{- fail "cnpg-client: podSelector (the application's matchLabels) is required for the network policy" -}}{{- end -}}
{{- if not $p.podSelector -}}{{- fail "cnpg-client: podSelector must not be empty" -}}{{- end -}}
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: {{ $p.name | default (printf "%s-cnpg-%s" .context.Release.Name $p.cluster) | trunc 63 | trimSuffix "-" }}
  namespace: {{ .context.Release.Namespace }}
spec:
  podSelector:
    matchLabels:
      {{- toYaml $p.podSelector | nindent 6 }}
  policyTypes:
    - Egress
  egress:
    - to:
        - podSelector:
            matchLabels:
              cnpg.io/cluster: {{ $p.cluster | quote }}
          {{- if $p.clusterNamespaceSelector }}
          namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: {{ $p.namespace | quote }}
          {{- end }}
      ports:
        - protocol: TCP
          port: {{ $p.port }}
{{- end -}}
