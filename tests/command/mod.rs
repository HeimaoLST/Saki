use super::*;

#[test]
fn literal_paths_multiline_content_and_escaped_slashes_remain_messages() {
    for text in [
        "hello /new",
        "/Users/me/src/main.rs",
        "/README.md",
        "/new\nthen explain",
        "  /help\r\n",
        "\n/new",
        "explain /help",
    ] {
        assert_eq!(
            parse(text, true).unwrap(),
            ParsedInput::Message(text.to_owned()),
            "{text:?}"
        );
    }
    assert_eq!(
        parse("  //new", true).unwrap(),
        ParsedInput::Message("  /new".into())
    );
    assert_eq!(
        parse("///tmp/file", false).unwrap(),
        ParsedInput::Message("//tmp/file".into())
    );
    assert!(completions("\n/new").is_empty());
    assert!(completions("//help").is_empty());
    assert!(completions("/rename title").is_empty());
}

#[test]
fn unknown_commands_invalid_arguments_and_images_never_fall_through_to_messages() {
    for text in ["/resuem", "/Help", "/"] {
        assert!(matches!(parse(text, false), Err(CommandError::Unknown(_))));
    }
    for text in [
        "/new other",
        "/help extra",
        "/sessions extra",
        "/resume",
        "/resume a b",
        "/rename   ",
    ] {
        assert!(
            matches!(parse(text, false), Err(CommandError::Usage(_))),
            "{text}"
        );
    }
    for text in [
        "/new",
        "/sessions",
        "/help",
        "/rename Compiler work",
        "/resume id",
    ] {
        assert_eq!(parse(text, true), Err(CommandError::Attachments));
    }
    assert_eq!(
        parse("  /rename 编译器 work  ", false).unwrap(),
        ParsedInput::Command(Command::Rename {
            title: "编译器 work".into()
        })
    );
}
