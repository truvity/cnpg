{{/* The cluster's prefix: its own, or <namespace>/<name>/. */}}
{{- define "cnpg-backup-access.prefix" -}}
{{- if .prefix -}}{{ .prefix }}{{- else -}}{{ printf "%s/%s/" .namespace .name }}{{- end -}}
{{- end -}}

{{/* The archive ServiceAccount: the cluster's own, named like the cluster. */}}
{{- define "cnpg-backup-access.archiveSA" -}}
{{- if and .archive .archive.serviceAccount -}}{{ .archive.serviceAccount }}{{- else -}}{{ .name }}{{- end -}}
{{- end -}}

{{/* Restore access is on unless the entry says restore.enabled: false. */}}
{{- define "cnpg-backup-access.restoreOn" -}}
{{- if or (not .restore) (not (hasKey .restore "enabled")) .restore.enabled -}}true{{- end -}}
{{- end -}}

{{- define "cnpg-backup-access.restoreSA" -}}
{{- if and .restore .restore.serviceAccount -}}{{ .restore.serviceAccount }}{{- else -}}{{ printf "%s-restore" .name }}{{- end -}}
{{- end -}}

{{- define "cnpg-backup-access.restoreNamespace" -}}
{{- if and .restore .restore.namespace -}}{{ .restore.namespace }}{{- else -}}{{ .namespace }}{{- end -}}
{{- end -}}

{{/* The identity policy of one tenant role, as the compact JSON that
backupstore.TenantRolePolicy renders for an SSE-S3, SingleProject bucket:
the same statements, Sids, actions and AES256 condition, keys in sorted order
(Go sorts map keys, so does toJson). A test compares the two byte for byte.
Called with (dict "bucket" name "partition" p "prefix" prefix "purpose"
"archive"|"restore"). The tenant is always the first and only one, so the Sid
number is 1. */}}
{{- define "cnpg-backup-access.policy" -}}
{{- $arn := printf "arn:%s:s3:::%s" .partition .bucket }}
{{- $res := printf "%s/%s*" $arn .prefix }}
{{- $n := printf "1%s" (title .purpose) }}
{{- $stmts := list }}
{{- if eq .purpose "archive" }}
{{- $stmts = append $stmts (dict "Sid" (printf "Put%s" $n) "Effect" "Allow" "Action" (list "s3:PutObject") "Resource" $res "Condition" (dict "StringEquals" (dict "s3:x-amz-server-side-encryption" "AES256"))) }}
{{- $stmts = append $stmts (dict "Sid" (printf "Object%s" $n) "Effect" "Allow" "Action" (list "s3:AbortMultipartUpload" "s3:ListMultipartUploadParts" "s3:GetObject" "s3:PutObjectTagging") "Resource" $res) }}
{{- else }}
{{- $stmts = append $stmts (dict "Sid" (printf "Object%s" $n) "Effect" "Allow" "Action" (list "s3:GetObject") "Resource" $res) }}
{{- end }}
{{- $stmts = append $stmts (dict "Sid" (printf "List%s" $n) "Effect" "Allow" "Action" (list "s3:ListBucket") "Resource" $arn) }}
{{- dict "Version" "2012-10-17" "Statement" $stmts | toJson -}}
{{- end -}}

{{/* The trust policy: EKS Pod Identity, pinned to the source account. */}}
{{- define "cnpg-backup-access.trust" -}}
{{- dict "Version" "2012-10-17" "Statement" (list (dict "Effect" "Allow" "Principal" (dict "Service" "pods.eks.amazonaws.com") "Action" (list "sts:AssumeRole" "sts:TagSession") "Condition" (dict "StringEquals" (dict "aws:SourceAccount" .accountID)))) | toJson -}}
{{- end -}}

{{/* metadata of an ACK object: name, namespace, common labels and
annotations. Called with (dict "root" $ "name" n "namespace" ns "annotations" a). */}}
{{- define "cnpg-backup-access.metadata" -}}
name: {{ .name }}
namespace: {{ .namespace }}
{{- with .root.Values.commonLabels }}
labels:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .annotations }}
annotations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}
