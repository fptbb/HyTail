package org.hytail.app;

import java.lang.reflect.Method;
import java.lang.reflect.Modifier;

/** Invocation by name keeps the UI independent of gomobile's naming conventions. */
final class Mobile {
    private static final String BINDING = "org.hytail.androidbridge.Androidbridge";
    private Mobile() {}
    static String invoke(String name, String... values) throws Exception {
        Class<?> binding = Class.forName(BINDING);
        for (Method method : binding.getMethods()) {
            if (!Modifier.isStatic(method.getModifiers()) || !method.getName().equalsIgnoreCase(name)
                    || method.getParameterCount() != values.length) continue;
            Class<?>[] types = method.getParameterTypes();
            boolean stringsOnly = true;
            for (Class<?> type : types) if (type != String.class) stringsOnly = false;
            if (!stringsOnly || method.getReturnType() != String.class) continue;
            return (String) method.invoke(null, (Object[]) values);
        }
        throw new NoSuchMethodException("Missing Go binding: " + name + " in " + BINDING);
    }
}
