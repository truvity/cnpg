{{/*
Resolve the serverName for recovery source.
Defaults to clusterName if not explicitly set.
*/}}
{{- define "cnpg-cluster.sourceServerName" -}}
{{- /* No dig: .Values is chartutil.Values, which dig rejects with an
interface conversion panic the moment a recovery render runs. Chained
defaults survive absent intermediate keys. */ -}}
{{- $src := ((.Values.bootstrap | default dict).recovery | default dict).source | default dict -}}
{{- $src.serverName | default .Values.clusterName -}}
{{- end -}}

{{/*
The hba posture: hostssl only, never trust, implicit reject at the end.
hba is FIRST-MATCH, so password (scram) lines are emitted per password
role BEFORE the catch-all cert line — a leading blanket cert rule would
shadow scram and break every password login. Projects may APPEND via
postgresql.extra_pg_hba, never replace.
*/}}
{{- define "cnpg-cluster.pgHba" -}}
{{- if .Values.bootstrap.initdb.owner }}
- hostssl all {{ .Values.bootstrap.initdb.owner }} all scram-sha-256
{{- end }}
{{- range .Values.roles }}
{{- if and (eq (.auth | default "cert") "password") (ne .name $.Values.bootstrap.initdb.owner) }}
- hostssl all {{ .name | replace "-" "_" }} all scram-sha-256
{{- end }}
{{- end }}
{{- $peopleRoles := include "cnpg-cluster.peopleRoles" . | trim }}
{{- if $peopleRoles }}
- hostssl all {{ $peopleRoles }} all cert map=people
{{- end }}
{{- range ((.Values.postgresql.pgHba | default dict).beforeCatchAll | default list) }}
- {{ . }}
{{- end }}
- hostssl all all all cert
{{- range .Values.postgresql.extra_pg_hba }}
- {{ . }}
{{- end }}
{{- end -}}

{{/*
The distinct database roles people map to, sorted, comma-joined (an hba
user list). Empty when `people` is empty. Validates the whole people
surface and every `map=` an hba line names: a map with no rows would
reject every connection that reaches it, silently, at connect time.
*/}}
{{- define "cnpg-cluster.peopleRoles" -}}
{{- $roles := list -}}
{{- range (.Values.people | default list) -}}
{{- if hasPrefix "pg_" .role -}}
{{- fail (printf "people: role %q starts with pg_, which PostgreSQL reserves for its predefined roles" .role) -}}
{{- end -}}
{{- $roles = append $roles .role -}}
{{- end -}}
{{- $lines := concat (((.Values.postgresql.pgHba | default dict).beforeCatchAll | default list)) (.Values.postgresql.extra_pg_hba | default list) -}}
{{- range $lines -}}
{{- range (regexFindAll "map=[^ \t]+" . -1) -}}
{{- $name := trimPrefix "map=" . -}}
{{- if ne $name "people" -}}
{{- fail (printf "pg_hba line uses map=%s, but the only pg_ident map this chart renders is \"people\" (from values `people`)" $name) -}}
{{- end -}}
{{- if not $roles -}}
{{- fail "pg_hba line uses map=people, but `people` is empty: a map with no rows rejects every connection that reaches it" -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- uniq (sortAlpha $roles) | join "," -}}
{{- end -}}

{{/* pg_ident rows for the people map. */}}
{{- define "cnpg-cluster.pgIdent" -}}
{{- range (.Values.people | default list) }}
- people {{ .email }} {{ .role }}
{{- end }}
{{- end -}}

{{/* The per-database client CA Secret and the replication Secret. */}}
{{- define "cnpg-cluster.replicationEnabled" -}}
{{- if ((.Values.replication | default dict).enabled) -}}true{{- end -}}
{{- end -}}

{{/* Issuer and CA names; cluster-scoped, so namespace-qualified. */}}
{{- define "cnpg-cluster.trustName" -}}
{{- printf "cnpg-%s-%s" .Values.namespace .Values.clusterName -}}
{{- end -}}

{{/* Validates and returns the trust namespace. */}}
{{- define "cnpg-cluster.trustNamespace" -}}
{{- $ns := (.Values.trust | default dict).namespace | default "" -}}
{{- if not $ns -}}
{{- fail "trust.namespace is required with trust.ca.enabled or trust.bundle.enabled: the CA Secret must live in cert-manager's cluster resource namespace, which is also the namespace trust-manager reads sources from" -}}
{{- end -}}
{{- $ns -}}
{{- end -}}

{{/*
Default s3 prefix = {namespace}/{clusterName}/ — the backup permission
model's write scope.
*/}}
{{- define "cnpg-cluster.s3Prefix" -}}
{{- .Values.backup.s3Prefix | default (printf "%s/%s" .Values.namespace .Values.clusterName) -}}
{{- end -}}

{{/*
serverTLS is on when an issuer is named. Chained defaults, not dig:
.Values is chartutil.Values (see sourceServerName above).
*/}}
{{- define "cnpg-cluster.serverTLSEnabled" -}}
{{- $tls := .Values.serverTLS | default dict -}}
{{- if ($tls.issuerRef | default dict).name -}}true{{- end -}}
{{- end -}}
