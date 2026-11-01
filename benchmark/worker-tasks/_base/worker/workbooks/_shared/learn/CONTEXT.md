# Stage: learn (shared, mandatory last stage of every workbook)

The run is not finished until this stage has written its report. It exists so the friction of every run is captured in one place instead of being fixed ad hoc or forgotten.

## Input
Everything that happened in this run: commands that failed, instructions that were wrong or missing, detours, tools that misbehaved, and what finally worked.

## Process
1. Write `journal/runs/<yyyy-mm-dd>-<workbook>-<slug>.md` from the template in `journal/runs/README.md` (the date is the day of the run, the slug a few kebab-case words naming the component and the symptom or change). Fill every section, even if the answer is "none".
2. Fix what is safe to fix now, in the same run: a wrong fact in a component README. Reference each fix from the report (`fixed now: yes`).
3. Unrelated defects seen on the way → `TODO.md`. Nothing else goes there.
4. One line in `journal/<yyyy-mm>.md` (create the file if the month is new): `- dd.MM.yyyy <org>/<component>: <workbook> <slug> - <outcome>; run report journal/runs/<file>`.
5. Last action: commit and push this repo (CLAUDE.md rule 6).

## Output
The run report file, the journal line, any in-run fixes, the pushed commit.

## Completion
Report the path of the run report and the pushed sha in the final answer.
