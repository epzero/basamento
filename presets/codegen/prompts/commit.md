# Commit

Commit exactly the workspace the last verification tested. A bootstrap run
commits it as the first commit of a new repository on its default branch. An
iterate run creates codegen/<run id> from the base branch and commits there; it
never writes the default branch.
