package mapreduce

type MapReduceGroup struct {
	Group []MR
}

func Add[T any, U any, V any](mrg *MapReduceGroup, mr *MapReduce[T, U, V]) {
	//todo:append
	mrg.Group = append(mrg.Group)
}

func Run[T any, U any, V any](mrg MapReduceGroup, mr MapReduce[T, U, V]) (V, error) {
	return mr.Run()
}
