# Contributing to Nexus

Thank you for your interest in contributing!

## Getting Started

1. Fork the repository
2. Create a feature branch: `git checkout -b feature/my-feature`
3. Make your changes
4. Run tests: `go test ./...`
5. Commit with a clear message
6. Push and open a Pull Request

## Development Setup

```bash
# Clone
git clone https://github.com/anthropic/nexus.git
cd nexus

# Backend only (headless mode)
go build -o nexus .
./nexus --headless

# Full desktop app
cd frontend && npm install && cd ..
wails3 build
```

## Code Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Keep functions focused and small
- Write tests for new functionality
- Use meaningful variable names

## Reporting Issues

- Use GitHub Issues
- Include OS, Go version, and steps to reproduce
- For agent-specific issues, note which CLI agent (Claude/Codex/Gemini) and version

## License

By contributing, you agree that your contributions will be licensed under the Apache 2.0 License.
