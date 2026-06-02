package domain

type OrganizationId string
type ProjectId string
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
	OrganizationId OrganizationId
	ProjectId      ProjectId
	Environment    EnvironmentName
	Folder         FolderName
}

type Secret struct {
	Scope SecretScope
	Key   SecretKey
}
