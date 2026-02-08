# Contributing to Camflow

First off, thank you for considering contributing! Projects like this stay alive because of work like yours. 

As the sole author and maintainer, I appreciate your patience. I’m happy to review bug reports, feature requests, and pull requests, but please follow these guidelines to help me process them efficiently.

## 🛠 Ways to Contribute

### 1. Reporting Bugs
If you find a bug, please open an **Issue**. To help me fix it quickly, try to include:
* A clear, descriptive title.
* The version of Go you are using (eg, the output of `camflow version`).
* Steps to reproduce the behavior.
* What you expected to happen vs. what actually happened.

### 2. Suggesting Features
I'm open to new ideas! Please open an **Issue** first to discuss a feature before you start coding it. This ensures the suggestion aligns with the project’s goals and saves you from doing work that might not be merged.

### 3. Submitting Pull Requests (PRs)
I welcome PRs for bug fixes, documentation, and agreed-upon features. 

## 🏗 Development Workflow

This is a standard Go project. I expect contributions to follow idiomatic Go practices.

### Technical Requirements
* **Go Version:** Ensure you are using a supported version of Go (refer to `go.mod`).
* **Formatting:** All code must be formatted with `gofmt` (or `goimports`).
* **Linting:** Please run `go vet ./...` before submitting.
* **Testing:** Ensure all tests pass by running `go test ./...`. New features or bug fixes **must** include corresponding tests.

### PR Checklist
1. **Branching:** Create a descriptive branch name (e.g., `fix/connection-leak` or `feat/add-logging`).
2. **Clean Commits:** Use clear, imperative commit messages (e.g., "Add support for S3 storage" instead of "fixed stuff").
3. **Documentation:** Update any relevant comments or README sections if you change functionality.
4. **Tidy Modules:** Run `go mod tidy` to ensure your dependencies are clean.

## 📜 Code Style
I aim for "Effective Go" standards:
* **Errors:** Handle errors explicitly. Wrap them if it adds useful context.
* **Naming:** Use `camelCase` for internal variables and `PascalCase` for exported ones. Keep names concise but meaningful.
* **Comments:** Exported functions, types, and constants should have descriptive doc comments.

## 🤝 Community & Expectations
As I am managing this project solo, it might take me a few days (or longer) to respond to issues or PRs. Your contribution is important to me, and I will get to it as soon as I can!