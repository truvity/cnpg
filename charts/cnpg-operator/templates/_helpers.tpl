{{/* Labels on every rendered object: the chart's own, then the caller's. */}}
{{- define "cnpg-operator.labels" -}}
app.kubernetes.io/name: cnpg-operator
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/* Annotations: commonAnnotations, then the object's own. Empty renders nothing. */}}
{{- define "cnpg-operator.annotations" -}}
{{- $a := merge (dict) (.own | default dict) (.root.Values.commonAnnotations | default dict) -}}
{{- with $a -}}
annotations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}
