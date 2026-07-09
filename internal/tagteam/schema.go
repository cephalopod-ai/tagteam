package tagteam

const ReviewSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["schema_version", "verdict", "summary", "findings", "test_suggestions"],
  "properties": {
    "schema_version": {
      "type": "integer",
      "const": 1
    },
    "verdict": {
      "type": "string",
      "enum": ["pass", "needs_changes"]
    },
    "summary": {
      "type": "string"
    },
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["severity", "file", "line", "issue", "fix"],
        "properties": {
          "severity": {
            "type": "string",
            "enum": ["blocker", "major", "minor", "nit"]
          },
          "file": {
            "type": "string"
          },
          "line": {
            "type": "integer"
          },
          "issue": {
            "type": "string"
          },
          "fix": {
            "type": "string"
          }
        },
        "additionalProperties": false
      }
    },
    "test_suggestions": {
      "type": "array",
      "items": {
        "type": "string"
      }
    }
  },
  "additionalProperties": false
}`

const WorkPlanSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["schema_version", "summary", "packages", "selected_package"],
  "properties": {
    "schema_version": {
      "type": "integer",
      "const": 1
    },
    "summary": {
      "type": "string"
    },
    "packages": {
      "type": "array",
      "minItems": 1,
      "items": {
        "type": "object",
        "required": ["id", "title", "goal", "acceptance", "validation"],
        "properties": {
          "id": {
            "type": "string"
          },
          "title": {
            "type": "string"
          },
          "goal": {
            "type": "string"
          },
          "allowed_scope": {
            "type": "array",
            "items": {
              "type": "string"
            }
          },
          "acceptance": {
            "type": "array",
            "minItems": 1,
            "items": {
              "type": "string"
            }
          },
          "validation": {
            "type": "array",
            "minItems": 1,
            "items": {
              "type": "string"
            }
          }
        },
        "additionalProperties": false
      }
    },
    "selected_package": {
      "type": "string"
    },
    "defer": {
      "type": "array",
      "items": {
        "type": "string"
      }
    }
  },
  "additionalProperties": false
}`

const OrchestrationAdvisorySchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["schema_version", "recommendation", "target_mode", "reason", "confidence"],
  "properties": {
    "schema_version": {
      "type": "integer",
      "const": 1
    },
    "recommendation": {
      "type": "string",
      "enum": ["keep", "simplify", "escalate"]
    },
    "target_mode": {
      "type": "string",
      "enum": ["supervisor", "relay"]
    },
    "reason": {
      "type": "string",
      "maxLength": 500
    },
    "confidence": {
      "type": "string",
      "enum": ["low", "medium", "high"]
    }
  },
  "additionalProperties": false
}`

func ScoutSchemaForRepair() string {
	return `{
  "type": "object",
  "required": ["relevant_files", "likely_entry_points", "existing_patterns", "risks", "suggested_tests"],
  "properties": {
    "schema_version": {"type": "integer"},
    "mode": {"type": "string"},
    "summary": {"type": "string"},
    "relevant_files": {"type": "array", "items": {"type": "string"}},
    "likely_entry_points": {"type": "array", "items": {"type": "string"}},
    "existing_patterns": {"type": "array", "items": {"type": "string"}},
    "risks": {"type": "array", "items": {"type": "string"}},
    "suggested_tests": {"type": "array", "items": {"type": "string"}},
    "retrieval_queries": {"type": "array", "items": {"type": "string"}},
    "evidence": {"type": "array"},
    "items": {"type": "array"},
    "do_not_block": {"type": "boolean"}
  }
}`
}
