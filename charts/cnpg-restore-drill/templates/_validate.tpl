{{/* Everything a schema cannot say, refused before anything renders. Every
rule has a fixture under tests/invalid/cnpg-restore-drill/. */}}
{{- define "cnpg-restore-drill.validate" -}}
{{- $names := dict }}
{{- $sources := dict }}
{{- $exporter := include "cnpg-restore-drill.exporterName" . }}
{{- $drills := include "cnpg-restore-drill.resolved" . | fromJsonArray }}
{{- $accounts := dict $exporter "the exporter" }}
{{- range $drills }}
{{- $_ := set $accounts (include "cnpg-restore-drill.runnerName" .drillName) (printf "the runner of %s" .drillName) }}
{{- end }}
{{- range $i, $e := $drills }}
{{- $id := printf "drills[%d] (%s/%s)" $i $e.namespace $e.cluster }}
{{- if not $e.storageClass }}
{{- fail (printf "%s: storageClass is required (defaults.storageClass or on the entry): a class that deletes its volumes" $id) }}
{{- end }}
{{- if or (gt (len $e.drillName) 50) (not (regexMatch "^[a-z]([-a-z0-9]*[a-z0-9])?$" $e.drillName)) }}
{{- fail (printf "%s: drill name %q must be a DNS label of at most 50 characters (a CloudNativePG Cluster name; the default is <cluster>-restore)" $id $e.drillName) }}
{{- end }}
{{- if and (eq $e.namespace $.Release.Namespace) (eq $e.drillName $e.cluster) }}
{{- fail (printf "%s: the drill name is the source cluster itself" $id) }}
{{- end }}
{{- if hasKey $names $e.drillName }}
{{- fail (printf "%s: drill name %q is used twice (a drill Cluster name is unique in the release namespace)" $id $e.drillName) }}
{{- end }}
{{- $_ := set $names $e.drillName true }}
{{- $src := printf "%s/%s" $e.namespace $e.cluster }}
{{- if hasKey $sources $src }}
{{- fail (printf "%s: the source %s is listed twice" $id $src) }}
{{- end }}
{{- $_ := set $sources $src true }}
{{- if hasKey $accounts $e.drillName }}
{{- fail (printf "%s: drill name %q is the ServiceAccount of %s; a runner or the exporter must never hold a restore identity" $id $e.drillName (get $accounts $e.drillName)) }}
{{- end }}
{{- $fields := splitList " " ($e.schedule | trim) }}
{{- if and (not (hasPrefix "@" $e.schedule)) (ne (len (compact $fields)) 5) }}
{{- fail (printf "%s: schedule %q must have five fields" $id $e.schedule) }}
{{- end }}
{{- if and (not (hasPrefix "@" $e.schedule)) (regexMatch "^[0-9]+$" (index (compact $fields) 1)) }}
{{- $hour := index (compact $fields) 1 | atoi }}
{{- if and (ge $hour 1) (le $hour 3) }}
{{- fail (printf "%s: schedule %q runs inside 01:00-04:00 UTC, the base backup window" $id $e.schedule) }}
{{- end }}
{{- end }}
{{- $a := $e.archive | default dict }}
{{- if and $a.prefix (not $a.bucket) }}
{{- fail (printf "%s: archive.prefix needs archive.bucket" $id) }}
{{- end }}
{{- if and $a.prefix (or (not (hasSuffix "/" $a.prefix)) (hasPrefix "/" $a.prefix)) }}
{{- fail (printf "%s: archive.prefix %q must be <namespace>/<cluster>/: no leading slash, a trailing one" $id $a.prefix) }}
{{- end }}
{{- if and $a.serverName $a.generation }}
{{- fail (printf "%s: archive.serverName and archive.generation are two spellings of one value: set one" $id) }}
{{- end }}
{{- if or (and $a.generation (not $a.project)) (and $a.project (not $a.generation)) }}
{{- fail (printf "%s: archive.project and archive.generation go together (serverName <project>-pg-<generation>)" $id) }}
{{- end }}
{{- end }}
{{- if and .Values.networkPolicy.enabled (not .Values.networkPolicy.operator) }}
{{- fail "networkPolicy.enabled is true but networkPolicy.operator is empty: selecting the drill instances for ingress closes their status port to the operator, and the drill never becomes healthy" }}
{{- end }}
{{- end -}}
