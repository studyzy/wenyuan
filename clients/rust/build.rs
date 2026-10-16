//! Build-time client generation.
//!
//! The client under `src/` is a thin hand-written wrapper; everything that
//! mirrors the HTTP API is generated here from `clients/openapi.json`, the
//! OpenAPI document this repository derives and commits.
//!
//! The document is OpenAPI 3.1 (it is produced by the server's own schema
//! layer), while the generator consumed here -- `progenitor`, i.e. the
//! `openapiv3` parser plus the `typify` schema compiler -- speaks 3.0. Rather
//! than keeping a second, hand-maintained copy of the document in the 3.0
//! spelling, this build script rewrites the parts of the 3.1 document that
//! 3.0 cannot express, in place, and each rewrite is documented with the
//! reason it exists. Regenerating therefore means `cargo build`: the spec is
//! re-read on every build in which it changed (see the `rerun-if-changed`
//! directive in `main`).

use std::collections::HashMap;
use std::env;
use std::fs;
use std::path::PathBuf;

/// The generator version the emitted code is written against, surfaced in the
/// build log so a surprising diff in the generated file can be traced back to
/// the tool that produced it.
const GENERATOR: &str = "progenitor 0.11 (openapiv3 + typify)";

fn main() {
    let crate_dir = PathBuf::from(env::var("CARGO_MANIFEST_DIR").expect("CARGO_MANIFEST_DIR"));
    let spec_path = crate_dir
        .parent()
        .expect("clients/rust has a parent directory")
        .join("openapi.json");

    // The generated client is a pure function of the document, so a build only
    // has to rerun when the document (or this script, which cargo tracks on
    // its own) changes.
    println!("cargo:rerun-if-changed={}", spec_path.display());
    println!("cargo:rerun-if-changed=build.rs");

    let raw = fs::read_to_string(&spec_path).unwrap_or_else(|err| {
        panic!(
            "cannot read the OpenAPI document at {}: {err}",
            spec_path.display()
        )
    });
    let mut spec: serde_json::Value =
        serde_json::from_str(&raw).expect("clients/openapi.json is valid JSON");

    println!("cargo:warning=generating the WenYuan client with {GENERATOR}");

    // Order matters. The nullable rewrite has to happen before the string-union
    // collapse, because the collapse intentionally looks at the non-null arms
    // only; the enum rename has to happen before generation, since it is typify
    // that would otherwise merge the two same-titled enums. Dropping whole
    // operations comes last, so the rewrites above never have to special-case a
    // path that is about to disappear.
    downgrade_openapi_version(&mut spec);
    null_arms_to_nullable(&mut spec);
    collapse_string_only_unions(&mut spec);
    disambiguate_same_titled_inline_enums(&mut spec);
    drop_multipart_operations(&mut spec);
    drop_bodyless_error_responses(&mut spec);

    let spec: openapiv3::OpenAPI = serde_json::from_value(spec)
        .expect("the rewritten document is a valid OpenAPI 3.0 document");

    let mut generator = progenitor::Generator::default();
    let tokens = generator
        .generate_tokens(&spec)
        .expect("the document is within what the generator can model");

    // Re-format the emitted tokens: `generate_tokens` produces a compact
    // one-liner, which is unusable as a debugging artifact when a build fails
    // inside generated code.
    let ast = syn::parse2(tokens).expect("the generator emits parsable Rust");
    let code = prettyplease::unparse(&ast);
    let code = fix_optional_header_values(&code);

    let out =
        PathBuf::from(env::var("OUT_DIR").expect("OUT_DIR")).join("wenyuan_client_generated.rs");
    fs::write(&out, code).expect("the generated source is writable");
}

/// Restate `openapi: 3.1.x` as `3.0.3`.
///
/// The `openapiv3` types this build script deserializes into are the 3.0
/// object model: a 3.1 document fails to parse at the root, before any schema
/// is looked at. Every other rewrite below makes the body of the document
/// agree with the version it now claims.
fn downgrade_openapi_version(spec: &mut serde_json::Value) {
    let declared = spec
        .get("openapi")
        .and_then(|v| v.as_str())
        .unwrap_or_default()
        .to_owned();
    assert!(
        declared.starts_with("3.1"),
        "expected an OpenAPI 3.1 document, found {declared:?}: either the \
         derivation changed or these rewrites are no longer needed"
    );
    spec["openapi"] = serde_json::Value::String("3.0.3".to_owned());
}

/// Turn `anyOf: [<schema>, {"type": "null"}]` into `<schema>` plus
/// `nullable: true`.
///
/// This is the 3.1 spelling of an optional value versus the 3.0 one. Left
/// alone, an `anyOf` whose arms include `null` is compiled by typify into an
/// untagged union enum with a unit `Null` variant, which the generated
/// serializer has to write out as a nested union -- the resulting client does
/// not round-trip the plain `null` and `"value"` the server sends.
///
/// A union of two or more non-null arms keeps its `anyOf` (minus the null arm)
/// and gains `nullable`, so the arms stay distinguishable while the
/// null-ability moves to the 3.0 keyword.
fn null_arms_to_nullable(value: &mut serde_json::Value) {
    if let serde_json::Value::Object(object) = value {
        let has_null_arm = object
            .get("anyOf")
            .and_then(|v| v.as_array())
            .is_some_and(|arms| arms.iter().any(is_null_schema));

        if has_null_arm {
            let arms = object
                .remove("anyOf")
                .and_then(|v| match v {
                    serde_json::Value::Array(arms) => Some(arms),
                    _ => None,
                })
                .unwrap_or_default();
            let mut arms: Vec<serde_json::Value> = arms
                .into_iter()
                .filter(|arm| !is_null_schema(arm))
                .collect();

            if arms.len() == 1 {
                // A single remaining arm: inline it, so the property is a plain
                // nullable value rather than a one-arm union.
                if let serde_json::Value::Object(arm) = arms.remove(0) {
                    for (key, val) in arm {
                        object.insert(key, val);
                    }
                }
            } else {
                object.insert("anyOf".to_owned(), serde_json::Value::Array(arms));
            }
            object.insert("nullable".to_owned(), serde_json::Value::Bool(true));
        }
    }
    recurse(value, null_arms_to_nullable);
}

/// Collapse a union whose arms are all `string` (regardless of `format`) into
/// a plain `{"type": "string"}`.
///
/// Where 3.1 writes "a datetime or a string" as `anyOf: [{string, date-time},
/// {string}]`, typify emits a struct with a flattened subtype -- and a flatten
/// of a primitive is a *runtime* serialization error ("can only flatten
/// structs and maps"), not a compile error, so it would only surface in a
/// user's program. A client cannot observe the `format` distinction anyway:
/// the wire value is a JSON string either way, and the generated type is
/// `String`. Collapsing keeps the property serializable and the client usable.
fn collapse_string_only_unions(value: &mut serde_json::Value) {
    if let serde_json::Value::Object(object) = value {
        let all_strings = object
            .get("anyOf")
            .and_then(|v| v.as_array())
            .is_some_and(|arms| {
                !arms.is_empty()
                    && arms
                        .iter()
                        .all(|arm| arm.get("type").and_then(|t| t.as_str()) == Some("string"))
            });
        if all_strings {
            object.remove("anyOf");
            object.insert(
                "type".to_owned(),
                serde_json::Value::String("string".to_owned()),
            );
        }
    }
    recurse(value, collapse_string_only_unions);
}

/// Give same-titled inline enums with different value sets distinct titles.
///
/// Inline enums carry the `title` of the field that declares them, and the
/// document has several fields named `outcome`, `kind`, `mode` and friends
/// whose allowed values differ. typify names an inline enum after its title,
/// so the second and later ones would silently reuse the first type: a value
/// the document allows would then be rejected by the generated deserializer.
/// Renaming a later collision to `{Schema}{Title}` keeps both enums, and the
/// first occurrence keeps the title it had, so no other generated name moves.
fn disambiguate_same_titled_inline_enums(spec: &mut serde_json::Value) {
    let Some(schemas) = spec
        .pointer_mut("/components/schemas")
        .and_then(|v| v.as_object_mut())
    else {
        return;
    };

    // title -> the value set the first enum with that title allows
    let mut claimed: HashMap<String, Vec<String>> = HashMap::new();
    for (schema_name, schema) in schemas.iter_mut() {
        let Some(properties) = schema.get_mut("properties").and_then(|v| v.as_object_mut()) else {
            continue;
        };
        for property in properties.values_mut() {
            let Some(title) = property.get("title").and_then(|t| t.as_str()) else {
                continue;
            };
            let Some(values) = enum_values(property) else {
                continue;
            };
            match claimed.get(title) {
                None => {
                    claimed.insert(title.to_owned(), values);
                }
                // Same title, same values: one type is what we want.
                Some(first) if *first == values => {}
                Some(_) => {
                    let prefix: String = schema_name
                        .chars()
                        .filter(|c| c.is_alphanumeric())
                        .collect();
                    property["title"] = serde_json::Value::String(format!("{prefix}{title}"));
                }
            }
        }
    }
}

/// Drop every operation whose request body is `multipart/form-data`.
///
/// These are the file-upload operations (`files/retain`, the document/bank
/// import endpoints). The generator models JSON request bodies only: a
/// multipart operation yields either a compile error or a body type that
/// cannot carry a file part, so the honest client is a spec without them. The
/// operation is removed, not its path: a path that still has other methods
/// (the document-transfer download shares its path with the multipart import)
/// keeps them, and only a path left with no operations at all is removed.
fn drop_multipart_operations(spec: &mut serde_json::Value) {
    let Some(paths) = spec.get_mut("paths").and_then(|v| v.as_object_mut()) else {
        return;
    };

    let mut emptied = Vec::new();
    for (path, item) in paths.iter_mut() {
        let Some(operations) = item.as_object_mut() else {
            continue;
        };
        let multipart: Vec<String> = operations
            .iter()
            .filter(|(method, operation)| is_http_method(method) && has_multipart_body(operation))
            .map(|(method, _)| method.clone())
            .collect();
        for method in &multipart {
            println!("cargo:warning=client omits {method} {path}: multipart/form-data");
            operations.remove(method);
        }
        if !operations.keys().any(|key| is_http_method(key)) {
            emptied.push(path.clone());
        }
    }
    for path in emptied {
        paths.remove(&path);
    }
}

/// Drop error responses that declare no body.
///
/// The generated method carries at most one error type per operation, and the
/// server's typed error is the `422` every bank-scoped operation documents.
/// A bodyless `404` next to it would either be a second error type (which the
/// generator refuses) or shadow the useful one. Nothing is lost by dropping
/// it: a status the document does not declare still reaches the caller as the
/// generated `Error::UnexpectedResponse`, with the status code and headers
/// intact.
fn drop_bodyless_error_responses(spec: &mut serde_json::Value) {
    let Some(paths) = spec.get_mut("paths").and_then(|v| v.as_object_mut()) else {
        return;
    };
    for item in paths.values_mut() {
        let Some(operations) = item.as_object_mut() else {
            continue;
        };
        for (method, operation) in operations.iter_mut() {
            if !is_http_method(method) {
                continue;
            }
            let operation_id = operation
                .get("operationId")
                .and_then(|v| v.as_str())
                .unwrap_or("<anonymous>")
                .to_owned();
            let Some(responses) = operation
                .get_mut("responses")
                .and_then(|v| v.as_object_mut())
            else {
                continue;
            };
            responses.retain(|status, response| {
                let is_error = matches!(status.chars().next(), Some('4' | '5'));
                if is_error && response.get("content").is_none() {
                    println!("cargo:warning=client omits the bodyless {status} of {operation_id}");
                    return false;
                }
                true
            });
        }
    }
}

/// Patch the generated code where it stringifies an optional header value.
///
/// An optional header parameter is threaded through the generated method as
/// `Option<&str>`, but the emitted header-append call has `.to_string()` on the
/// `Option` itself (`header_map.append("authorization", value.to_string()...)`),
/// and an `Option` has no such method: the file does not compile, once per
/// operation with an optional header. Defaulting to the empty string is the
/// behaviour the server sees anyway -- an absent `Authorization` header and an
/// empty one are both "no credentials" -- and it is the only shape of fix that
/// keeps the parameter optional.
///
/// The pattern is anchored on the header append rather than on a header name,
/// so it also covers any other optional header the document grows.
fn fix_optional_header_values(code: &str) -> String {
    code.replace(
        r#"value.to_string().try_into()?)"#,
        r#"value.unwrap_or_default().to_string().try_into()?)"#,
    )
}

/// Run `f` over every nested value of `value`.
fn recurse(value: &mut serde_json::Value, f: fn(&mut serde_json::Value)) {
    match value {
        serde_json::Value::Object(object) => {
            for nested in object.values_mut() {
                f(nested);
            }
        }
        serde_json::Value::Array(items) => {
            for nested in items.iter_mut() {
                f(nested);
            }
        }
        _ => {}
    }
}

fn is_null_schema(value: &serde_json::Value) -> bool {
    value.get("type").and_then(|t| t.as_str()) == Some("null")
}

fn is_http_method(key: &str) -> bool {
    matches!(
        key,
        "get" | "post" | "put" | "delete" | "patch" | "head" | "options" | "trace"
    )
}

fn has_multipart_body(operation: &serde_json::Value) -> bool {
    operation
        .get("requestBody")
        .and_then(|b| b.get("content"))
        .and_then(|c| c.as_object())
        .is_some_and(|content| content.contains_key("multipart/form-data"))
}

/// The sorted `enum` of a schema, as comparable strings, if it declares one.
fn enum_values(schema: &serde_json::Value) -> Option<Vec<String>> {
    let mut values: Vec<String> = schema
        .get("enum")?
        .as_array()?
        .iter()
        .map(|v| v.to_string())
        .collect();
    values.sort();
    Some(values)
}
