- `just secrets k8s-secret <bot> <file>` copies a bot's credentials from
  `secrets/bots/<bot>.sops.yaml` into a sops-encrypted Kubernetes Secret manifest as
  `stringData["<bot>.yaml"]`, ready to mount as `credentials_dir`. Nothing is decrypted to
  disk or printed, and other keys in the Secret are kept.
