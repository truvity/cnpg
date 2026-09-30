{{/*
Full name for the primary database's object.
Truncated to 63 chars for DNS-label compliance. PostgreSQL names may
carry underscores; Kubernetes names may not, so the metadata name is
slugged while spec.name stays verbatim.
*/}}
{{- define "cnpg-database.fullname" -}}
{{- printf "%s-%s" .Values.clusterName (.Values.databaseName | replace "_" "-") | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* The namespace every object is rendered into (the release namespace). */}}
{{- define "cnpg-database.namespace" -}}
{{- .Release.Namespace -}}
{{- end -}}

{{/* The default client-certificate issuer: the naming contract with the platform. */}}
{{- define "cnpg-database.defaultIssuer" -}}
{{- printf "cnpg-%s-%s" .Release.Namespace .Values.clusterName -}}
{{- end -}}
