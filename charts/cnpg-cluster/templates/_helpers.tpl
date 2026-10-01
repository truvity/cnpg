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
{{/* The Secret the chart-issued replication Certificate writes. Never
<cluster>-replication, -ca or -server: those are the names the CNPG
operator generates itself, and cert-manager will not overwrite an
operator-owned Secret. */}}
{{- define "cnpg-cluster.replicationSecretName" -}}
{{- $r := .Values.replication | default dict -}}
{{- if $r.secretName -}}{{ $r.secretName | trim }}{{- else -}}{{ .Values.clusterName }}-replication-tls{{- end -}}
{{- end }}

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

{{/*
Names-only server TLS: serverTLS.existingSecret + existingCASecret name
Secrets somebody else owns. Prints "true" when both are set; fails when only
one is, or when the chart's own Certificate (issuerRef) is asked for too.
Rendering no object for these Secrets is the point, so nothing else reads
the names but cluster.yaml.
*/}}
{{- define "cnpg-cluster.serverTLSExisting" -}}
{{- $tls := .Values.serverTLS | default dict -}}
{{- $tlsName := trim ($tls.existingSecret | default "") -}}
{{- $caName := trim ($tls.existingCASecret | default "") -}}
{{- if or $tlsName $caName -}}
{{- if not (and $tlsName $caName) -}}
{{- fail "serverTLS.existingSecret and serverTLS.existingCASecret go together: set both or neither" -}}
{{- end -}}
{{- if include "cnpg-cluster.serverTLSEnabled" . -}}
{{- fail "serverTLS.existingSecret/existingCASecret cannot be combined with serverTLS.issuerRef: either the chart requests the server certificate or it uses Secrets that already exist" -}}
{{- end -}}
true
{{- end -}}
{{- end -}}

{{/*
The ObjectStore the Cluster's plugin and the ScheduledBackup name: the one
the platform made (backup.objectStoreName) or, when empty, the one this
chart renders from backup.bucketName.
*/}}
{{- define "cnpg-cluster.objectStoreName" -}}
{{- .Values.backup.objectStoreName | default (printf "%s-objectstore" .Values.clusterName) -}}
{{- end -}}

{{/*
Archiving is on when backup.enabled and the cluster has somewhere to
archive to: a bucket this chart describes, or an ObjectStore somebody else
owns. Prints "true" or nothing.
*/}}
{{- define "cnpg-cluster.backupActive" -}}
{{- if and .Values.backup.enabled (or .Values.backup.bucketName .Values.backup.objectStoreName) -}}true{{- end -}}
{{- end -}}

{{/*
backup.objectStoreName names an ObjectStore this chart does not render, so
nothing the chart would have written into one may be set beside it: it would
be silently ignored. serverName is required, because the default
(clusterName) is a guess about somebody else's archive layout, and a wrong
guess starts a second timeline there.
*/}}
{{- define "cnpg-cluster.backupGuard" -}}
{{- $b := .Values.backup -}}
{{- if $b.objectStoreName -}}
{{- if $b.bucketName -}}
{{- fail "backup.objectStoreName and backup.bucketName are mutually exclusive: name an existing ObjectStore, or describe the bucket for this chart to render one" -}}
{{- end -}}
{{- if not $b.serverName -}}
{{- fail "backup.objectStoreName needs backup.serverName: this cluster's directory inside the referenced archive (the default, clusterName, is only right for an archive this chart created)" -}}
{{- end -}}
{{- range $k := list "s3Prefix" "endpoint" "existingSecret" -}}
{{- if index $b $k -}}
{{- fail (printf "backup.%s describes an ObjectStore this chart renders, and backup.objectStoreName names one it does not: unset backup.%s" $k $k) -}}
{{- end -}}
{{- end -}}
{{- if $b.endpointCA -}}
{{- fail "backup.endpointCA describes an ObjectStore this chart renders, and backup.objectStoreName names one it does not: unset backup.endpointCA" -}}
{{- end -}}
{{- end -}}
{{- end -}}
