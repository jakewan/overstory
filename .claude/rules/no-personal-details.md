# Keep Personal Details Out

This is a public repository. Keep personal and identifying details out of everything that ships or is published — code, comments, test fixtures, commit messages, issues, and pull requests.

## What not to introduce

- Real names and email addresses.
- Absolute home-directory or machine-specific paths (e.g. `/home/<user>/…`, `/Users/<user>/…`). Use relative paths, repo-relative paths, or placeholders.
- Machine, host, or network names.
- Private or internal project names, and references to private repositories or internal tooling.
- Credentials of any kind — tokens, keys, passwords — even example-looking ones.

## Sensible boundaries

- **Necessary attribution is fine.** The LICENSE copyright line and normal git authorship are expected and stay.
- **Generic examples are fine.** Illustrative manifest entries, placeholder `owner/repo` slugs, and example config that carry no real personal data are encouraged.
- When an example needs a path or name, use an obvious placeholder (`owner/repo`, `/path/to/manifest.yml`, `example.com`, `your-name`) rather than a real one.

## Why

A public repo is read by anyone. Scattering personal or environment-specific details leaks information, dates the content to one machine or person, and makes the project read as someone's private workspace rather than a reusable tool. Keeping it impersonal keeps it portable and respectful of privacy.
