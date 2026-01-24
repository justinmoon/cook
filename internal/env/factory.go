package env

import "fmt"

// NewBackend creates a Backend based on the given type and config.
func NewBackend(backendType Type, cfg Config) (Backend, error) {
	switch backendType {
	case TypeLocal, "":
		return NewLocalBackend(cfg), nil
	case TypeDocker:
		return NewDockerBackend(cfg)
	case TypeModal:
		return nil, fmt.Errorf("modal backend not yet implemented")
	default:
		return nil, fmt.Errorf("unknown backend type: %s", backendType)
	}
}

// NewBackendFromPath creates a Backend for an existing checkout path.
// This is useful when the environment is already set up.
func NewBackendFromPath(backendType Type, workDir string) (Backend, error) {
	switch backendType {
	case TypeLocal, "":
		return NewLocalBackendFromPath(workDir), nil
	case TypeDocker:
		// For Docker, we need the container ID to reconnect
		// This path is used for existing environments, so we need to look up the container
		return nil, fmt.Errorf("docker backend requires container ID for existing environments")
	case TypeModal:
		return nil, fmt.Errorf("modal backend not yet implemented")
	default:
		return nil, fmt.Errorf("unknown backend type: %s", backendType)
	}
}

// NewDockerBackendFromContainerID creates a Docker backend from an existing container.
func NewDockerBackendFromContainerID(containerID, hostWorkDir string) (Backend, error) {
	return NewDockerBackendFromExisting(containerID, hostWorkDir)
}
