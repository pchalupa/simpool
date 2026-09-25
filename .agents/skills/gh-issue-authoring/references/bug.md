# <Area>: <what breaks>

> Sentence case, under 70 characters. Name the screen, package, or command plus the broken behaviour. Avoid "doesn't work".

## What happens

> One or two sentences: what the user does, what goes wrong, who it hits. Someone who stops here knows whether to pick it up.

## Steps to reproduce

> Three to eight numbered steps from a known state (fresh clone, logged out, seeded account). One action per step, literal values only.

1. <action>
2. <action>
3. <action>

## Expected

> The one behaviour that should happen at the last step, as an observable fact.

## Actual

> What happens instead: the error verbatim, the wrong value, the crash. Facts only — a theory about the cause goes in "What happens" or a comment.

```text
<paste the error, stack trace, or log lines>
```

## Environment

| Field           | Value                                     |
| --------------- | ----------------------------------------- |
| Version         | <4.12.0 / commit abc1234>                 |
| Platform        | <macOS 26.1 / Ubuntu 24.04 / Chrome 142>  |
| Runtime         | <Node 24.4.0 / Python 3.13>               |
| Environment     | <local / staging / production>            |
| Reproducibility | <5 of 5 attempts / intermittent, 2 of 10> |

> Drop rows that don't apply; always keep version, environment, and reproducibility.

## Regression

> Only when it used to work: the last good version and the first bad one, plus the suspected PR as `#123`. Delete this section otherwise.
