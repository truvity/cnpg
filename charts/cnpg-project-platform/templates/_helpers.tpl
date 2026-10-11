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

{{/* The sync-wave annotation for a wave value; nothing when the wave is empty. */}}
{{- define "cnpg-project-platform.waveAnnotations" -}}
{{- if .wave }}
argocd.argoproj.io/sync-wave: {{ .wave | quote }}
{{- else }}
{}
{{- end }}
{{- end -}}

{{/* The prune/delete guard annotation when guard is true; nothing otherwise. */}}
{{- define "cnpg-project-platform.guardAnnotations" -}}
{{- if .guard }}
argocd.argoproj.io/sync-options: Prune=false,Delete=false
{{- else }}
{}
{{- end }}
{{- end -}}
