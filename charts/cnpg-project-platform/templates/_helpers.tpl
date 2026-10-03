{{/* The metadata block of one object: labels (the chart's commonLabels, then
the entry's) and annotations (likewise). Called with (dict "root" $ "name" n
"namespace" ns "labels" extraLabels "annotations" extraAnnotations
"fixedLabels" labelsTheTemplateAlwaysSets). Nothing is added that the caller
did not name: no app.kubernetes.io labels, so an object a platform already
runs under another renderer keeps its metadata exactly. */}}
{{- define "cnpg-project-platform.metadata" -}}
name: {{ .name }}
namespace: {{ .namespace }}
{{- with (merge (dict) (.fixedLabels | default dict) (.labels | default dict) (.root.Values.commonLabels | default dict)) }}
labels:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with (merge (dict) (.annotations | default dict) (.root.Values.commonAnnotations | default dict)) }}
annotations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}

{{/* Whether an entry is rendered: absent `enabled` means true. */}}
{{- define "cnpg-project-platform.on" -}}
{{- if or (not (hasKey . "enabled")) .enabled -}}true{{- end -}}
{{- end -}}
