package containers

func GetNamespaces() [2]string {
	namespaces := [2]string{"default", "system"}
	return namespaces
}
