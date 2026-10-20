# Security policy

## Supported versions

Only the latest release of maket receives security fixes. Check the [Releases page](https://github.com/itsgitz/maket/releases) for the current version, and upgrade before you report an issue that may already be fixed.

## Reporting a vulnerability

Please do not open a public issue for a security problem.

Report it privately: go to the repository's **Security** tab and choose **Report a vulnerability**. This uses GitHub's private vulnerability reporting. Include what you found, the steps to reproduce it, the affected version, and the impact as you understand it.

The maintainers will look at the report through the same private thread and work with you on a fix.

## Scope

In scope:

- Authentication and session handling, including GitHub sign-in and the dashboard session cookie.
- Allowlist bypass, or any way to use maket as a User who is not on the Allowlist.
- Path traversal and unsafe zip handling during Upload.
- Preview isolation: one Prototype reading or affecting another, or Previews reaching the API or dashboard.
- API Key handling, including an API Key managing API Keys or reaching another User's Prototypes.

Out of scope: problems in a self-hosted deployment's own configuration (for example a weak `MAKET_GITHUB_CLIENT_SECRET` you chose), and reports that need a stolen API Key or a compromised GitHub account.
