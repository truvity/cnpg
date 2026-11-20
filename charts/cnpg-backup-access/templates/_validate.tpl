{{/* Everything a schema cannot say, refused before anything renders. The
schema keeps the types and the shapes of single values; this keeps the
relations (a name used twice) and gives every missing input a message that
names the key. Every rule has a fixture under tests/invalid/cnpg-backup-access/. */}}
{{- define "cnpg-backup-access.validate" -}}
{{- if .Values.clusters }}
{{- $g := .Values.global }}
{{- if not .Values.bucket.name }}{{ fail "bucket.name is required: the project's bucket in the backup account" }}{{ end }}
{{- if not $g.accountID }}{{ fail "global.accountID is required: the source account the Roles live in" }}{{ end }}
{{- if not $g.eksClusterName }}{{ fail "global.eksClusterName is required: the EKS cluster of the Pod Identity associations" }}{{ end }}
{{- if not $g.region }}{{ fail "global.region is required: the region of the EKS cluster, for the aws:SourceArn the trust policy pins" }}{{ end }}
{{- if not $g.rolePrefix }}{{ fail "global.rolePrefix is required: Role names are <rolePrefix>-<cluster>-archive|restore" }}{{ end }}
{{- $names := dict }}
{{- $prefixes := dict }}
{{- $sas := dict }}
{{- $roleNames := dict }}
{{- range $i, $c := .Values.clusters }}
{{- if not $c.name }}{{ fail (printf "clusters[%d].name is required" $i) }}{{ end }}
{{- if not $c.namespace }}{{ fail (printf "clusters[%d].namespace is required" $i) }}{{ end }}
{{- if hasKey $names $c.name }}
{{- fail (printf "clusters: %q appears twice (the Role names derive from the cluster name alone, so two namespaces cannot share it)" $c.name) }}
{{- end }}
{{- $_ := set $names $c.name true }}
{{- $prefix := include "cnpg-backup-access.prefix" $c }}
{{- if not (hasSuffix "/" $prefix) }}{{ fail (printf "clusters[%d] (%s): prefix %q must end in /" $i $c.name $prefix) }}{{ end }}
{{- if or (hasPrefix "/" $prefix) (contains "*" $prefix) (contains "?" $prefix) (eq $prefix "/") }}
{{- fail (printf "clusters[%d] (%s): prefix %q must be <namespace>/<cluster>/: no leading slash, no glob" $i $c.name $prefix) }}
{{- end }}
{{- if hasKey $prefixes $prefix }}
{{- fail (printf "clusters[%d] (%s): prefix %q is already used by cluster %q" $i $c.name $prefix (get $prefixes $prefix)) }}
{{- end }}
{{- $_ := set $prefixes $prefix $c.name }}
{{- range $p := list "archive" "restore" }}
{{- $rn := printf "%s-%s-%s" $g.rolePrefix $c.name $p }}
{{- if gt (len $rn) 64 }}{{ fail (printf "clusters[%d] (%s): the IAM role name %q is %d characters, the limit is 64" $i $c.name $rn (len $rn)) }}{{ end }}
{{- if hasKey $roleNames $rn }}{{ fail (printf "clusters[%d] (%s): the role name %q is already used by %s" $i $c.name $rn (get $roleNames $rn)) }}{{ end }}
{{- $_ := set $roleNames $rn (printf "clusters[%d] (%s)" $i $c.name) }}
{{- end }}
{{- /* roleNames: additional roles under explicit names. Each must be new (not a
default, not another cluster's), the restore one needs restore switched on, and
bindExplicit needs at least one explicit name to move the association to. */}}
{{- $rnm := $c.roleNames | default dict }}
{{- $anyExplicit := false }}
{{- range $p := list "archive" "restore" }}
{{- $en := get $rnm $p | default "" }}
{{- if $en }}
{{- $anyExplicit = true }}
{{- if and (eq $p "restore") (not (include "cnpg-backup-access.restoreOn" $c)) }}{{ fail (printf "clusters[%d] (%s): roleNames.restore is set but restore is disabled (restore.enabled: false)" $i $c.name) }}{{ end }}
{{- if gt (len $en) 64 }}{{ fail (printf "clusters[%d] (%s): roleNames.%s %q is %d characters, the limit is 64" $i $c.name $p $en (len $en)) }}{{ end }}
{{- if hasKey $roleNames $en }}{{ fail (printf "clusters[%d] (%s): roleNames.%s %q is already used by %s" $i $c.name $p $en (get $roleNames $en)) }}{{ end }}
{{- $_ := set $roleNames $en (printf "clusters[%d] (%s) roleNames.%s" $i $c.name $p) }}
{{- end }}
{{- end }}
{{- if and (get $rnm "bindExplicit") (not $anyExplicit) }}{{ fail (printf "clusters[%d] (%s): roleNames.bindExplicit is true but no explicit role name is set" $i $c.name) }}{{ end }}
{{- /* One ServiceAccount binds to at most one role per (namespace, name). */}}
{{- $asa := include "cnpg-backup-access.archiveSA" $c }}
{{- $akey := printf "%s/%s" $c.namespace $asa }}
{{- if hasKey $sas $akey }}{{ fail (printf "clusters[%d] (%s): ServiceAccount %q is already bound to a role by another entry" $i $c.name $akey) }}{{ end }}
{{- $_ := set $sas $akey true }}
{{- if include "cnpg-backup-access.restoreOn" $c }}
{{- $rkey := printf "%s/%s" (include "cnpg-backup-access.restoreNamespace" $c) (include "cnpg-backup-access.restoreSA" $c) }}
{{- if hasKey $sas $rkey }}{{ fail (printf "clusters[%d] (%s): ServiceAccount %q is already bound to a role by another entry" $i $c.name $rkey) }}{{ end }}
{{- $_ := set $sas $rkey true }}
{{- end }}
{{- end }}
{{- end }}
{{- end -}}
