package blob

// GateScenarioTags is the godog tag expression for the deterministic gate run.
const GateScenarioTags = "~@planned && ~@live && ~@eval && ~@journey && ~@slow && ~@quarantine"
