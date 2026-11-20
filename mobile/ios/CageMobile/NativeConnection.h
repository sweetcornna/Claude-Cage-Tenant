#import <Foundation/Foundation.h>
NS_ASSUME_NONNULL_BEGIN
@interface CMConnection : NSObject
+ (NSString *)defaultRelay;
@property(class, nonatomic, readonly) BOOL usesSimulatorIdentity;
+ (nullable NSString *)pairCode:(NSString *)code relay:(NSString *)relay error:(NSError **)error NS_SWIFT_NAME(pair(code:relay:));
+ (nullable NSString *)deviceFingerprint:(NSError **)error NS_SWIFT_NAME(deviceFingerprint());
+ (nullable instancetype)connectToRelay:(NSString *)relay room:(NSString *)room token:(NSString *)token
    pin:(NSString *)pin assets:(NSString *)assets error:(NSError **)error NS_SWIFT_NAME(connect(relay:room:token:pin:assets:));
+ (nullable instancetype)connectSavedRelay:(NSString *)relay lookup:(NSString *)lookup pin:(NSString *)pin
    assets:(NSString *)assets error:(NSError **)error NS_SWIFT_NAME(connectSaved(relay:lookup:pin:assets:));
@property(nonatomic, readonly) NSURL *launchURL;
- (void)suspend;
- (BOOL)resume;
- (void)close;
@end

/// A signed-in account on a multi-tenant Cage server portal. Every call
/// except the properties reaches the network: call off the main thread.
@interface CMServer : NSObject
+ (nullable NSString *)canonicalPortal:(NSString *)raw error:(NSError **)error NS_SWIFT_NAME(canonicalPortal(_:));
+ (nullable instancetype)signInPortal:(NSString *)portal user:(NSString *)user password:(NSString *)password
    error:(NSError **)error NS_SWIFT_NAME(signIn(portal:user:password:));
@property(nonatomic, readonly) NSString *portal;
@property(nonatomic, readonly) NSString *user;
- (nullable NSString *)virtualMachines:(NSError **)error NS_SWIFT_NAME(virtualMachines());
- (nullable NSString *)createVirtualMachine:(NSString *)name error:(NSError **)error NS_SWIFT_NAME(createVirtualMachine(name:));
- (nullable CMConnection *)openVirtualMachine:(NSString *)vmID assets:(NSString *)assets error:(NSError **)error NS_SWIFT_NAME(open(virtualMachine:assets:));
- (void)close;
@end
NS_ASSUME_NONNULL_END
