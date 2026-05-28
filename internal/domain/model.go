package domain

type OrganizationID string
type ProjectID string
type EnvironmentName string
type FolderName string
type SecretKey string

const (
	EnvironmentDev  EnvironmentName = "dev"
	EnvironmentTest EnvironmentName = "test"
	EnvironmentSim  EnvironmentName = "sim"
	EnvironmentProd EnvironmentName = "prod"
)

type SecretScope struct {
	OrganizationID OrganizationID
	ProjectID      ProjectID
	Environment    EnvironmentName
	Folder         FolderName
}

type Secret struct {
	Scope SecretScope
	Key   SecretKey
}
