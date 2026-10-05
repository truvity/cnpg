// Package backupstore deploys a cross-account backup bucket as one Pulumi
// ComponentResource: a versioned S3 bucket in a backup account that a
// workload account may write to and may never permanently delete from.
//
// What the component builds, in the account of the AWS provider:
//   - the bucket, versioned, encrypted with its own customer-managed KMS key
//     (rotation on), with every form of public access blocked;
//   - a bucket policy that admits a named writer role in the source account
//     (a glob), optionally one read-only role and any number of list-only
//     roles, and denies s3:DeleteObjectVersion to every principal outside
//     the backup account;
//   - a key policy that admits the same roles to the key and nothing else
//     in the source account: a second gate that survives a bucket-policy
//     mistake;
//   - a lifecycle configuration the caller supplies (the backstop behind
//     whatever retention the backup tool enforces itself);
//   - optionally S3 Object Lock in COMPLIANCE mode, so nobody, the backup
//     account included, can delete a version inside the window;
//   - optionally a same-account replica in another region, with its own
//     key, a replication role and the replication configuration.
//
// The bucket and the keys are protected by default (Args.Protect), and the
// keys are retained on delete: key destruction is a break-glass act, so
// Pulumi drops a key from state instead of scheduling its deletion.
//
// Every name, tag, boundary and description is a caller input. Nothing here
// defaults to one estate's value.
//
// See docs/reference.md for the children, their names and their aliases.
package backupstore
