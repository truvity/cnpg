{{/* Labels of every object; .component is runner, exporter or drill. */}}
{{- define "cnpg-restore-drill.labels" -}}
app.kubernetes.io/name: cnpg-restore-drill
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
app.kubernetes.io/component: {{ .component }}
{{- with .root.Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/* The selector of a component's pods. */}}
{{- define "cnpg-restore-drill.selector" -}}
app.kubernetes.io/name: cnpg-restore-drill
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{/* The runner of one drill (called with the drill name): one ServiceAccount
per drill, so a runner can touch only its own drill's objects and read only
its own source (component contract C14). */}}
{{- define "cnpg-restore-drill.runnerName" -}}{{ . }}-runner{{- end -}}
{{- define "cnpg-restore-drill.exporterName" -}}{{ .Release.Name }}-exporter{{- end -}}

{{/* The cnpgctl image: the release's digest pin when packaged, else image. */}}
{{- define "cnpg-restore-drill.image" -}}
{{- $p := .Values.images.cnpgctl | default dict -}}
{{- if $p.digest -}}
{{ with $p.registry }}{{ . }}/{{ end }}{{ $p.repository }}{{ with $p.tag }}:{{ . }}{{ end }}@{{ $p.digest }}
{{- else -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}
{{- end -}}

{{/* Every drills[] entry with `defaults` merged under it and the derived
values filled in, as a JSON array: drillName, schedule, destinationPath,
serverName. The default schedule is monthly at a day (1-28), hour (04-23 UTC,
never inside 01:00-04:00, around the 02:00 base backups) and minute taken from
a checksum of <namespace>/<cluster>, so drills are spread out and a render is
stable. */}}
{{- define "cnpg-restore-drill.resolved" -}}
{{- $out := list }}
{{- range $d := .Values.drills }}
{{- $e := mergeOverwrite (deepCopy $.Values.defaults) (deepCopy $d) }}
{{- $_ := set $e "drillName" ($e.drillName | default (printf "%s-restore" $e.cluster)) }}
{{- if not $e.schedule }}
{{- $h := adler32sum (printf "%s/%s" $e.namespace $e.cluster) | atoi }}
{{- $_ := set $e "schedule" (printf "%d %d %d * *" (mod (div $h 560) 60) (add 4 (mod (div $h 28) 20)) (add1 (mod $h 28))) }}
{{- end }}
{{- $a := $e.archive | default dict }}
{{- $dest := "" }}
{{- if $a.bucket }}
{{- $prefix := $a.prefix | default (printf "%s/%s/" $e.namespace $e.cluster) }}
{{- $dest = printf "s3://%s/%s" $a.bucket (trimSuffix "/" $prefix) }}
{{- end }}
{{- $_ := set $e "destinationPath" $dest }}
{{- $server := $a.serverName | default "" }}
{{- if and (not $server) $a.generation }}
{{- $server = printf "%s-pg-%s" $a.project $a.generation }}
{{- end }}
{{- $_ := set $e "serverName" $server }}
{{- $out = append $out $e }}
{{- end }}
{{- toJson $out }}
{{- end -}}

{{/* A toleration as cnpgctl's flag: key=value:Effect, or key:Effect for Exists. */}}
{{- define "cnpg-restore-drill.toleration" -}}
{{- if eq (.operator | default "Equal") "Exists" -}}
{{ .key }}:{{ .effect }}
{{- else -}}
{{ .key }}={{ .value | default "" }}:{{ .effect }}
{{- end -}}
{{- end -}}

{{/* The pod security posture shared by the runner and the exporter. */}}
{{- define "cnpg-restore-drill.podSecurity" -}}
runAsNonRoot: true
runAsUser: 65532
runAsGroup: 65532
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "cnpg-restore-drill.containerSecurity" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: [ALL]
{{- end -}}
