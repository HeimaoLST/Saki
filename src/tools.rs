#[macro_export]
macro_rules! registry_tool {
    ($registry:expr,[ $ ( $module:ident),* $(,)?]) => {
        $(
            pub mod $module;
            $registry.registry($module::tool());
        )*
    };
}
pub mod caculator;
pub mod tool_registry;
