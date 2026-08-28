## Detailed Document Definitions

### PRD (Product Requirements Document)

**Purpose**: Define business requirements and user value

**Includes**:

- Business requirements and user value
- Success metrics and KPIs (each metric specifies a numeric target and measurement method)
- User stories and use cases
- MoSCoW prioritization (Must/Should/Could/Won't)
- Acceptance criteria with sequential IDs (AC-001, AC-002, ...) for downstream traceability
- MVP and Future phase separation
- User journey diagram (required)
- Scope boundary diagram (required)

**Scope**: Business requirements, user value, success metrics, user stories, and prioritization only. Implementation details belong in Design Doc, technical selection rationale in ADR, phases and task breakdown in Work Plan.

### ADR (Architecture Decision Record)

**Purpose**: Record technical decision rationale and background

**Includes**:

- Decision (what was selected)
- Rationale (why that selection was made)
- Option comparison (minimum 3 options) and trade-offs
- Architecture impact
- Principled implementation guidelines (e.g., "Use dependency injection")

**Scope**: Decision, rationale, option comparison, architecture impact, and principled guidelines only. Implementation procedures and code examples belong in Design Doc, schedule and resource assignments in Work Plan.

### UI Specification

**Purpose**: Define UI structure, screen transitions, component decomposition, and interaction design for frontend features

**Includes**:

- Screen list and transition conditions
- Component decomposition with state x display matrix (default/loading/empty/error/partial)
- Interaction definitions linked to PRD acceptance criteria (EARS format)
- Prototype management (code-based prototypes as attachments, not source of truth)
- AC traceability from PRD to screens/components
- Existing component reuse map and design tokens
- Visual acceptance criteria (golden states, layout constraints)
- Accessibility requirements (keyboard, screen reader, contrast)

**Scope**: Screen structure, transitions, component decomposition, interaction design, and visual acceptance criteria only. Technical implementation and API contracts belong in Design Doc, test implementation in test skeleton generation output, schedule in Work Plan.

**Required Structural Elements**:

- At least one component with state x display matrix and interaction table
- AC traceability table mapping PRD ACs to screens/states
- Screen list with transition conditions
- Existing component reuse map (reuse/extend/new decisions)

**Prototype Code Handling**:

- Prototype code provided by user is placed in `docs/ui-spec/assets/{feature-name}/`
- Prototype is an attachment to UI Spec, never the source of truth
- UI Spec + Design Doc are the canonical specifications

### Design Document

**Purpose**: Define technical implementation methods in detail

**Includes**:

- **Existing codebase analysis** (required)
  - Implementation path mapping (both existing and new)
  - Integration point clarification (connection points with existing code even for new implementations)
- Technical implementation approach (vertical/horizontal/hybrid)
- **Technical dependencies and implementation constraints** (required implementation order)
- Interface and contract definitions
- Data flow and component design
- **Acceptance criteria (each criterion specifies a verifiable condition with pass/fail threshold)**
- Change impact map (clearly specify direct impact/indirect impact/no ripple effect)
- Complete enumeration of integration points
- Data contract clarification
- **Agreement checklist** (agreements with stakeholders)
- **Code inspection evidence** (inspected files/functions during investigation)
- **Field propagation map** (when fields cross component boundaries)
- **Data representation decision** (when introducing new structures)
- **Applicable standards** (explicit/implicit classification)
- **Prerequisite ADRs** (including common ADRs)
- **Verification Strategy** (required)
  - Correctness proof method (what "correct" means for this change, how it's verified, when)
  - Early verification point (first target to prove the approach works, success criteria, failure response)

**Required Structural Elements**:
```yaml
Change Impact Map:
  Change Target: [Component/Feature]
  Direct Impact: [Files/Functions]
  Indirect Impact: [Data format/Processing time]
  No Ripple Effect: [Unaffected features]

Interface Change Matrix:
  Existing: [Function/method/operation name]
  New: [Function/method/operation name]
  Conversion Required: [Yes/No]
  Compatibility Method: [Approach]
```

**Scope**: Technical implementation methods, interfaces, data flow, acceptance criteria, and verification strategy only. Technology selection rationale belongs in ADR, schedule and assignments in Work Plan.

### Work Plan

**Purpose**: Implementation task management and progress tracking

**Includes**:

- Task breakdown and dependencies (maximum 2 levels)
- Schedule and duration estimates
- **Include test skeleton file paths produced for this work plan** (integration and E2E)
- **Verification Strategy summary** (extracted from Design Doc)
- **Final Quality Assurance Phase (required)**
- Progress records (checkbox format)

**Scope**: Task breakdown, dependencies, schedule, verification strategy summary, and progress tracking only. Technical rationale belongs in ADR, design details in Design Doc.

**Phase Division Criteria** (adapt to implementation approach from Design Doc):

**When Vertical Slice selected**:

- Each phase = one value unit (feature, component, or migration target)
- Each phase includes its own implementation + verification per Verification Strategy

**When Horizontal Slice selected**:

1. **Phase 1: Foundation Implementation** - Contract definitions, interfaces/signatures, test preparation
2. **Phase 2: Core Feature Implementation** - Business logic, unit tests
3. **Phase 3: Integration Implementation** - External connections, presentation layer

**When Hybrid selected**:

- Combine vertical and horizontal as defined in Design Doc implementation approach

**All approaches**: Final phase is always Quality Assurance (acceptance criteria achievement, all tests passing, quality checks). Each phase's verification method follows Verification Strategy from Design Doc.

**Three Elements of Task Completion Definition**:

1. **Implementation Complete**: Code is functional
2. **Quality Complete**: Tests, static checks, linting pass
3. **Integration Complete**: Verified connection with other components

## Creation Process
