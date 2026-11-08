- The release docs no longer say to make the GHCR package public by hand: a package
  published from a public repository is public already, so the first-release step is
  now to check that the image pulls without logging in. The Docker page says when the
  `latest`, `X.Y` and `X` tags exist, and troubleshooting names a private package as the
  cause of `unauthorized` on pull.
